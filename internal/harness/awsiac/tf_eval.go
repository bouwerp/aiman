package awsiac

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/tryfunc"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

// A reference to a planned resource's attribute travels through
// expressions as a marker inside a string: two private-use runes around
// the reference's text (<stack>.<logical id>.<attr>), a key of
// tfEval.refs. String functions, templates and jsonencode
// keep it intact, so a policy built with jsonencode still names the ARN;
// tfEval.value turns the markers back into Value references.
const (
	tfRefOpen  = ''
	tfRefClose = ''
)

// tfRefEscape keeps a marker's text free of what JSON encoding rewrites
// (quotes, backslashes and, with HTML escaping, < > &), so a reference
// inside jsonencode comes out unchanged; url.PathUnescape reads it back.
var tfRefEscape = strings.NewReplacer("%", "%25", "\"", "%22", "\\", "%5C", "<", "%3C", ">", "%3E", "&", "%26")

// tfUnresolved marks a value aiman cannot know statically, with the
// reason; marks follow the value through functions and templates.
type tfUnresolved string

// tfSensitive marks a value from a sensitive variable.
type tfSensitive struct{}

// tfFunctions is the fixed function table; any other function is an
// unknown function to HCL and so unresolvable.
var tfFunctions = map[string]function.Function{
	"format":     stdlib.FormatFunc,
	"formatlist": stdlib.FormatListFunc,
	"lower":      stdlib.LowerFunc,
	"upper":      stdlib.UpperFunc,
	"title":      stdlib.TitleFunc,
	"trimspace":  stdlib.TrimSpaceFunc,
	"trim":       stdlib.TrimFunc,
	"trimprefix": stdlib.TrimPrefixFunc,
	"trimsuffix": stdlib.TrimSuffixFunc,
	"replace":    tfReplaceFunc,
	"substr":     stdlib.SubstrFunc,
	"split":      stdlib.SplitFunc,
	"join":       stdlib.JoinFunc,
	"concat":     stdlib.ConcatFunc,
	"merge":      stdlib.MergeFunc,
	"lookup":     stdlib.LookupFunc,
	"element":    stdlib.ElementFunc,
	"length":     tfLengthFunc,
	"keys":       stdlib.KeysFunc,
	"values":     stdlib.ValuesFunc,
	"contains":   stdlib.ContainsFunc,
	"distinct":   stdlib.DistinctFunc,
	"flatten":    stdlib.FlattenFunc,
	"coalesce":   tfCoalesceFunc,
	"try":        tryfunc.TryFunc,
	"can":        tryfunc.CanFunc,
	"tostring":   stdlib.MakeToFunc(cty.String),
	"tonumber":   stdlib.MakeToFunc(cty.Number),
	"tobool":     stdlib.MakeToFunc(cty.Bool),
	"tolist":     stdlib.MakeToFunc(cty.List(cty.DynamicPseudoType)),
	"toset":      stdlib.MakeToFunc(cty.Set(cty.DynamicPseudoType)),
	"tomap":      stdlib.MakeToFunc(cty.Map(cty.DynamicPseudoType)),
	"jsonencode": stdlib.JSONEncodeFunc,
	"jsondecode": stdlib.JSONDecodeFunc,
	"min":        stdlib.MinFunc,
	"max":        stdlib.MaxFunc,
}

// tfReplaceFunc is Terraform's replace: a substring wrapped in slashes is
// a regular expression.
var tfReplaceFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "str", Type: cty.String},
		{Name: "substr", Type: cty.String},
		{Name: "replace", Type: cty.String},
	},
	Type: function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		sub := args[1].AsString()
		if len(sub) > 1 && strings.HasPrefix(sub, "/") && strings.HasSuffix(sub, "/") {
			return stdlib.RegexReplace(args[0], cty.StringVal(sub[1:len(sub)-1]), args[2])
		}
		return stdlib.Replace(args[0], args[1], args[2])
	},
})

// tfCoalesceFunc is Terraform's coalesce: the first argument that is
// neither null nor an empty string.
var tfCoalesceFunc = function.New(&function.Spec{
	VarParam: &function.Parameter{Name: "vals", Type: cty.DynamicPseudoType, AllowUnknown: true, AllowDynamicType: true, AllowNull: true},
	Type:     function.StaticReturnType(cty.DynamicPseudoType),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		var keep []cty.Value
		for _, a := range args {
			if a.IsKnown() && !a.IsNull() && a.Type() == cty.String && a.AsString() == "" {
				continue
			}
			keep = append(keep, a)
		}
		if len(keep) == 0 {
			return cty.NilVal, errors.New("no non-null, non-empty-string arguments")
		}
		return stdlib.CoalesceFunc.Call(keep)
	},
})

// tfLengthFunc is Terraform's length: a string's characters or a
// collection's elements.
var tfLengthFunc = function.New(&function.Spec{
	Params: []function.Parameter{{Name: "value", Type: cty.DynamicPseudoType, AllowDynamicType: true, AllowUnknown: true}},
	Type:   function.StaticReturnType(cty.Number),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		v := args[0]
		if v.Type() == cty.String {
			if !v.IsKnown() {
				return cty.UnknownVal(cty.Number), nil
			}
			return stdlib.Strlen(v)
		}
		if v.Type().IsObjectType() {
			return cty.NumberIntVal(int64(len(v.Type().AttributeTypes()))), nil
		}
		return stdlib.LengthFunc.Call([]cty.Value{v})
	},
})

// tfEval evaluates one stack: module instances, their locals, resources
// and outputs, lazily and once each.
type tfEval struct {
	ctx      context.Context
	src      StackSource
	p        *Plan
	loader   *tfLoader
	errs     []error
	visiting map[string]bool
	refs     map[string]AttrRef
	insts    []*tfInst
	noted    map[string]bool

	pending     []*tfPending
	added       []*Resource
	attachments []*tfAttach
	functions   []FunctionEnv
	origin      map[string]*tfRes
	converted   int
}

func newTFEval(ctx context.Context, src StackSource, p *Plan, l *tfLoader) *tfEval {
	return &tfEval{ctx: ctx, src: src, p: p, loader: l, visiting: map[string]bool{}, refs: map[string]AttrRef{}, noted: map[string]bool{}, origin: map[string]*tfRes{}}
}

func (ev *tfEval) fail(err error) { ev.errs = append(ev.errs, err) }

// note records information once per text.
func (ev *tfEval) note(w Warning) {
	w.Stack = ev.src.Name
	if ev.noted[w.String()] {
		return
	}
	ev.noted[w.String()] = true
	ev.p.Note(w)
}

func (ev *tfEval) warn(w Warning) {
	w.Stack = ev.src.Name
	ev.p.Warn(w)
}

// marker is the in-string form of a reference.
func (ev *tfEval) marker(r AttrRef) string {
	text := tfRefEscape.Replace(r.String())
	ev.refs[text] = r
	return string(tfRefOpen) + text + string(tfRefClose)
}

// split turns a string holding markers into a Value.
func (ev *tfEval) split(s string) (Value, error) {
	var out Value
	for {
		i := strings.IndexRune(s, tfRefOpen)
		if i < 0 {
			if strings.ContainsRune(s, tfRefClose) {
				return nil, errors.New("a function cut a resource reference apart")
			}
			if out = Concat(out, tfLit(s)); out == nil {
				out = Value{}
			}
			return out, nil
		}
		j := strings.IndexRune(s[i:], tfRefClose)
		if j < 0 {
			return nil, errors.New("a function cut a resource reference apart")
		}
		r, ok := ev.refs[s[i+len(string(tfRefOpen)):i+j]]
		if !ok || strings.ContainsRune(s[:i], tfRefClose) {
			return nil, errors.New("a function cut a resource reference apart")
		}
		out = Concat(out, tfLit(s[:i]), Value{{Ref: &r}})
		s = s[i+j+len(string(tfRefClose)):]
	}
}

// tfLit is a literal part, none for empty text.
func tfLit(s string) Value {
	if s == "" {
		return nil
	}
	return Lit(s)
}

// tfUnknownVal is an unknown value carrying why.
func tfUnknownVal(format string, args ...any) cty.Value {
	return cty.DynamicVal.Mark(tfUnresolved(fmt.Sprintf(format, args...)))
}

// tfReasons lists the reasons marked anywhere in v.
func tfReasons(v cty.Value) []string {
	_, marks := v.UnmarkDeep()
	var out []string
	for m := range marks {
		if r, ok := m.(tfUnresolved); ok {
			out = append(out, string(r))
		}
	}
	sort.Strings(out)
	return out
}

func tfIsSensitive(v cty.Value) bool {
	_, marks := v.UnmarkDeep()
	_, ok := marks[tfSensitive{}]
	return ok
}

// tfErrUnknown says why a value is not wholly known.
func tfErrUnknown(v cty.Value) error {
	if r := tfReasons(v); len(r) > 0 {
		return errors.New(strings.Join(r, "; "))
	}
	return errors.New("depends on a value aiman cannot know without running terraform")
}

// value turns an evaluated value into a plan Value: strings keep their
// references, numbers and bools become text, collections JSON. A null is
// a nil Value; an unknown is an error naming why.
func (ev *tfEval) value(v cty.Value) (Value, error) {
	if !v.IsWhollyKnown() {
		return nil, tfErrUnknown(v)
	}
	v, _ = v.UnmarkDeep()
	if v.IsNull() {
		return nil, nil
	}
	switch {
	case v.Type() == cty.String:
		return ev.split(v.AsString())
	case v.Type() == cty.Number:
		return Lit(tfNumber(v.AsBigFloat())), nil
	case v.Type() == cty.Bool:
		return Lit(strconv.FormatBool(v.True())), nil
	}
	b, err := ctyjson.Marshal(v, v.Type())
	if err != nil {
		return nil, err
	}
	return ev.split(string(b))
}

func tfNumber(f *big.Float) string {
	if f.IsInt() {
		return f.Text('f', 0)
	}
	return f.Text('g', -1)
}

// literal is a value's text when it holds no reference.
func (ev *tfEval) literal(v cty.Value) (string, error) {
	val, err := ev.value(v)
	if err != nil {
		return "", err
	}
	s, ok := val.Literal()
	if !ok {
		return "", fmt.Errorf("refers to %s, which aiman names only when it plans the resource", val.Refs()[0])
	}
	return s, nil
}

// tfFromGo converts a value the harness file gives (YAML or JSON decoded)
// into cty.
func tfFromGo(v any) (cty.Value, error) {
	switch t := v.(type) {
	case nil:
		return cty.NullVal(cty.DynamicPseudoType), nil
	case string:
		return cty.StringVal(t), nil
	case bool:
		return cty.BoolVal(t), nil
	case int:
		return cty.NumberIntVal(int64(t)), nil
	case int64:
		return cty.NumberIntVal(t), nil
	case uint64:
		return cty.NumberUIntVal(t), nil
	case float64:
		return cty.NumberFloatVal(t), nil
	case []any:
		if len(t) == 0 {
			return cty.EmptyTupleVal, nil
		}
		out := make([]cty.Value, len(t))
		for i, e := range t {
			c, err := tfFromGo(e)
			if err != nil {
				return cty.NilVal, err
			}
			out[i] = c
		}
		return cty.TupleVal(out), nil
	case map[string]any:
		if len(t) == 0 {
			return cty.EmptyObjectVal, nil
		}
		out := map[string]cty.Value{}
		for k, e := range t {
			c, err := tfFromGo(e)
			if err != nil {
				return cty.NilVal, err
			}
			out[k] = c
		}
		return cty.ObjectVal(out), nil
	}
	return cty.NilVal, fmt.Errorf("a value of type %T is not supported", v)
}

// rootVars sets the root module's variables: default, then
// terraform.tfvars, terraform.tfvars.json, *.auto.tfvars(.json) in lexical
// order, then the stack's var_files, then vars (already merged with the
// scenario's). A variable with no value is an error naming it.
func (ev *tfEval) rootVars(mod *tfModule) (map[string]cty.Value, error) {
	given := map[string]cty.Value{}
	auto, err := ev.loader.autoVarFiles(mod.dir)
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, f := range append(auto, ev.src.VarFiles...) {
		vals, names, err := ev.loader.varFile(f)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, n := range names {
			if mod.variable(n) == nil {
				ev.note(Warning{Resource: "var." + n, File: f, Message: "the variable is not declared; the value is ignored"})
				continue
			}
			given[n] = vals[n]
		}
	}
	for _, n := range sortedKeys(ev.src.Vars) {
		if mod.variable(n) == nil {
			ev.warn(Warning{Resource: "var." + n, Message: "vars names a variable the stack does not declare; the value is ignored"})
			continue
		}
		v, err := tfFromGo(ev.src.Vars[n])
		if err != nil {
			errs = append(errs, fmt.Errorf("terraform: vars %s: %w", n, err))
			continue
		}
		given[n] = v
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return ev.setVars(mod, "", given)
}

// setVars converts the given values (else defaults) to each variable's
// type. where names the module call for errors ("" is the root).
func (ev *tfEval) setVars(mod *tfModule, where string, given map[string]cty.Value) (map[string]cty.Value, error) {
	out := map[string]cty.Value{}
	var errs []error
	var validated []string
	for _, v := range mod.variables {
		val, ok := given[v.name]
		if !ok || (val.IsKnown() && !val.IsMarked() && val.IsNull()) {
			if v.def == cty.NilVal {
				if ok {
					val = cty.NullVal(v.ty)
				} else {
					errs = append(errs, fmt.Errorf("terraform: %svariable %s has no value: give it in vars, var_files or a tfvars file (%s)", where, v.name, tfAt(v.rng)))
					continue
				}
			} else {
				val = v.def
			}
		}
		if v.typed {
			if v.defaults != nil {
				val = v.defaults.Apply(val)
			}
			cv, err := convert.Convert(val, v.ty)
			if err != nil {
				errs = append(errs, fmt.Errorf("terraform: %svariable %s: %v (%s)", where, v.name, err, tfAt(v.rng)))
				continue
			}
			val = cv
		}
		if v.sensitive {
			val = val.Mark(tfSensitive{})
		}
		if v.validations > 0 {
			validated = append(validated, "var."+v.name)
		}
		out[v.name] = val
	}
	if len(validated) > 0 {
		ev.note(Warning{Resource: strings.TrimSuffix(where, "."), File: mod.dir, Message: "validation blocks are not checked: " + strings.Join(validated, ", ")})
	}
	return out, errors.Join(errs...)
}

// tfInst is one module instance: the root, or one instance of a module
// call. prefix is its address with a trailing dot (module.a[0].).
type tfInst struct {
	ev      *tfEval
	mod     *tfModule
	prefix  string
	depth   int
	vars    map[string]cty.Value
	locals  map[string]cty.Value
	outputs map[string]cty.Value
	res     map[string]cty.Value
	keys    map[string]tfExpansion
	calls   map[string]*tfCallInsts
}

// tfExpansion is a block's instances, or why they are not known.
type tfExpansion struct {
	keys []tfKey
	mode tfMode
	why  string
}

type tfMode int

const (
	tfSingle tfMode = iota
	tfCount
	tfForEach
)

// tfKey is one instance key: none, a count index or a for_each key.
type tfKey struct {
	mode  tfMode
	index int
	key   string
	value cty.Value
}

// suffix is the key as Terraform writes it in an address.
func (k tfKey) suffix() string {
	switch k.mode {
	case tfCount:
		return "[" + strconv.Itoa(k.index) + "]"
	case tfForEach:
		return "[" + strconv.Quote(k.key) + "]"
	}
	return ""
}

// extra is the instance's count or each object.
func (k tfKey) extra() map[string]cty.Value {
	switch k.mode {
	case tfCount:
		return map[string]cty.Value{"count": cty.ObjectVal(map[string]cty.Value{"index": cty.NumberIntVal(int64(k.index))})}
	case tfForEach:
		return map[string]cty.Value{"each": cty.ObjectVal(map[string]cty.Value{"key": cty.StringVal(k.key), "value": k.value})}
	}
	return nil
}

func (ev *tfEval) newInst(mod *tfModule, prefix string, depth int, vars map[string]cty.Value) *tfInst {
	return &tfInst{ev: ev, mod: mod, prefix: prefix, depth: depth, vars: vars,
		locals: map[string]cty.Value{}, outputs: map[string]cty.Value{}, res: map[string]cty.Value{},
		keys: map[string]tfExpansion{}, calls: map[string]*tfCallInsts{}}
}

// enter guards a lazily evaluated node against cycles; a cycle is an
// error and the node evaluates as unresolved.
func (in *tfInst) enter(node string) (leave func(), ok bool) {
	id := fmt.Sprintf("%p/%s", in, node)
	if in.ev.visiting[id] {
		in.ev.fail(fmt.Errorf("terraform: %s%s refers to itself through a cycle", in.prefix, node))
		return nil, false
	}
	in.ev.visiting[id] = true
	return func() { delete(in.ev.visiting, id) }, true
}

// eval evaluates an expression of this module instance. extra adds count,
// each or a dynamic block's iterator. Only what the expression refers to
// is evaluated.
func (in *tfInst) eval(expr hcl.Expression, extra map[string]cty.Value) (cty.Value, hcl.Diagnostics) {
	vars := map[string]cty.Value{}
	for k, v := range extra {
		vars[k] = v
	}
	locals := map[string]cty.Value{}
	modules := map[string]cty.Value{}
	data := map[string]map[string]cty.Value{}
	resources := map[string]map[string]cty.Value{}
	for _, tr := range expr.Variables() {
		root := tr.RootName()
		if _, ok := extra[root]; ok {
			continue
		}
		name := tfAttrName(tr, 1)
		switch root {
		case "var":
			vars["var"] = in.varObject()
		case "local":
			if _, ok := in.mod.locals[name]; ok {
				locals[name] = in.local(name)
			}
		case "module":
			if in.mod.call(name) != nil {
				modules[name] = in.moduleValue(name)
			}
		case "data":
			typ, dname := name, tfAttrName(tr, 2)
			if typ == "" || dname == "" {
				continue
			}
			if data[typ] == nil {
				data[typ] = map[string]cty.Value{}
			}
			data[typ][dname] = tfUnknownVal("data.%s.%s is a data source, which aiman does not read", typ, dname)
		case "path":
			vars["path"] = cty.ObjectVal(map[string]cty.Value{
				"module": cty.StringVal(in.mod.dir), "root": cty.StringVal(in.ev.src.Path), "cwd": cty.StringVal(in.ev.src.Path),
			})
		case "terraform":
			vars["terraform"] = cty.ObjectVal(map[string]cty.Value{"workspace": cty.StringVal("default")})
		case "count", "each", "self":
		default:
			if in.mod.resource(root, name) != nil {
				if resources[root] == nil {
					resources[root] = map[string]cty.Value{}
				}
				resources[root][name] = in.resourceValue(root, name)
			} else if in.mod.hasResourceType(root) && resources[root] == nil {
				resources[root] = map[string]cty.Value{}
			}
		}
	}
	if len(locals) > 0 {
		vars["local"] = cty.ObjectVal(locals)
	}
	if len(modules) > 0 {
		vars["module"] = cty.ObjectVal(modules)
	}
	if len(data) > 0 {
		d := map[string]cty.Value{}
		for typ, m := range data {
			d[typ] = cty.ObjectVal(m)
		}
		vars["data"] = cty.ObjectVal(d)
	}
	for typ, m := range resources {
		vars[typ] = cty.ObjectVal(m)
	}
	return expr.Value(&hcl.EvalContext{Variables: vars, Functions: tfFunctions})
}

// tfAttrName is the attribute name at step i of a traversal, or "".
func tfAttrName(tr hcl.Traversal, i int) string {
	if len(tr) <= i {
		return ""
	}
	switch s := tr[i].(type) {
	case hcl.TraverseAttr:
		return s.Name
	case hcl.TraverseIndex:
		if s.Key.Type() == cty.String && s.Key.IsKnown() && !s.Key.IsNull() {
			return s.Key.AsString()
		}
	}
	return ""
}

// evalValue evaluates and folds diagnostics into an unresolved value.
func (in *tfInst) evalValue(expr hcl.Expression, extra map[string]cty.Value) cty.Value {
	v, diags := in.eval(expr, extra)
	if diags.HasErrors() {
		return tfUnknownVal("%s", tfDiagText(diags))
	}
	return v
}

func (in *tfInst) varObject() cty.Value {
	if len(in.vars) == 0 {
		return cty.EmptyObjectVal
	}
	return cty.ObjectVal(in.vars)
}

func (in *tfInst) local(name string) cty.Value {
	if v, ok := in.locals[name]; ok {
		return v
	}
	leave, ok := in.enter("local." + name)
	if !ok {
		return tfUnknownVal("local.%s is part of a cycle", name)
	}
	defer leave()
	v := in.evalValue(in.mod.locals[name].Expr, nil)
	in.locals[name] = v
	return v
}

func (in *tfInst) output(o *tfOutput) cty.Value {
	if v, ok := in.outputs[o.name]; ok {
		return v
	}
	leave, ok := in.enter("output." + o.name)
	if !ok {
		return tfUnknownVal("output %s is part of a cycle", o.name)
	}
	defer leave()
	v := in.evalValue(o.expr, nil)
	if o.sensitive {
		v = v.Mark(tfSensitive{})
	}
	in.outputs[o.name] = v
	return v
}

// outputsValue is the instance's outputs as an object.
func (in *tfInst) outputsValue() cty.Value {
	if len(in.mod.outputs) == 0 {
		return cty.EmptyObjectVal
	}
	m := map[string]cty.Value{}
	for _, o := range in.mod.outputs {
		m[o.name] = in.output(o)
	}
	return cty.ObjectVal(m)
}

// expand evaluates a block's count or for_each into instance keys.
func (in *tfInst) expand(node string, count, forEach hcl.Expression) tfExpansion {
	if e, ok := in.keys[node]; ok {
		return e
	}
	leave, ok := in.enter(node + "#expand")
	if !ok {
		return tfExpansion{why: node + " is part of a cycle"}
	}
	defer leave()
	e := in.expandNow(count, forEach)
	in.keys[node] = e
	return e
}

func (in *tfInst) expandNow(count, forEach hcl.Expression) tfExpansion {
	switch {
	case count != nil:
		v, diags := in.eval(count, nil)
		if diags.HasErrors() {
			return tfExpansion{why: "count: " + tfDiagText(diags)}
		}
		if !v.IsWhollyKnown() {
			return tfExpansion{why: "count " + tfErrUnknown(v).Error()}
		}
		v, _ = v.UnmarkDeep()
		n, err := convert.Convert(v, cty.Number)
		if err != nil || n.IsNull() {
			return tfExpansion{why: "count is not a number"}
		}
		i, acc := n.AsBigFloat().Int64()
		if acc != big.Exact || i < 0 || i > tfMaxInstances {
			return tfExpansion{why: fmt.Sprintf("count must be a whole number from 0 to %d", tfMaxInstances)}
		}
		e := tfExpansion{mode: tfCount}
		for j := 0; j < int(i); j++ {
			e.keys = append(e.keys, tfKey{mode: tfCount, index: j})
		}
		return e
	case forEach != nil:
		v, diags := in.eval(forEach, nil)
		if diags.HasErrors() {
			return tfExpansion{why: "for_each: " + tfDiagText(diags)}
		}
		return in.forEachKeys(v)
	}
	return tfExpansion{keys: []tfKey{{}}}
}

// forEachKeys reads a for_each value: a map or object (keys and values) or
// a set of strings (each value its key), in key order.
func (in *tfInst) forEachKeys(v cty.Value) tfExpansion {
	if !v.IsKnown() {
		return tfExpansion{why: "for_each " + tfErrUnknown(v).Error()}
	}
	v, marks := v.Unmark()
	if v.IsNull() {
		return tfExpansion{why: "for_each is null"}
	}
	ty := v.Type()
	e := tfExpansion{mode: tfForEach}
	switch {
	case ty.IsMapType() || ty.IsObjectType():
		for it := v.ElementIterator(); it.Next(); {
			k, val := it.Element()
			e.keys = append(e.keys, tfKey{mode: tfForEach, key: k.AsString(), value: val.WithMarks(marks)})
		}
	case ty.IsSetType():
		if !v.IsWhollyKnown() {
			return tfExpansion{why: "for_each " + tfErrUnknown(v).Error()}
		}
		for it := v.ElementIterator(); it.Next(); {
			_, val := it.Element()
			val, _ = val.UnmarkDeep()
			if val.Type() != cty.String || val.IsNull() {
				return tfExpansion{why: "for_each over a set needs a set of strings"}
			}
			e.keys = append(e.keys, tfKey{mode: tfForEach, key: val.AsString(), value: val})
		}
	default:
		return tfExpansion{why: "for_each needs a map or a set of strings, not " + ty.FriendlyName()}
	}
	if len(e.keys) > tfMaxInstances {
		return tfExpansion{why: fmt.Sprintf("for_each has more than %d instances", tfMaxInstances)}
	}
	for _, k := range e.keys {
		if strings.ContainsRune(k.key, tfRefOpen) {
			return tfExpansion{why: "for_each keys refer to resource attributes, which terraform knows only after apply"}
		}
	}
	sort.Slice(e.keys, func(i, j int) bool { return e.keys[i].key < e.keys[j].key })
	return e
}

// tfModelled are the resource types references can read attributes of,
// by their plan kind.
var tfModelled = map[string]Kind{
	"aws_dynamodb_table":         KindTable,
	"aws_s3_bucket":              KindBucket,
	"aws_sqs_queue":              KindQueue,
	"aws_sns_topic":              KindTopic,
	"aws_sns_topic_subscription": KindSubscription,
}

// resourceValue is what a reference to a resource reads: an object of
// reference markers for a modelled type, per instance (a tuple for count,
// an object by key for for_each); an unresolved value otherwise.
func (in *tfInst) resourceValue(typ, name string) cty.Value {
	addr := typ + "." + name
	if v, ok := in.res[addr]; ok {
		return v
	}
	r := in.mod.resource(typ, name)
	e := in.expand(addr, r.count, r.forEach)
	var v cty.Value
	switch {
	case e.why != "":
		v = tfUnknownVal("%s%s: %s", in.prefix, addr, e.why)
	case e.mode == tfCount:
		if len(e.keys) == 0 {
			v = cty.EmptyTupleVal
		} else {
			vals := make([]cty.Value, len(e.keys))
			for i, k := range e.keys {
				vals[i] = in.instanceValue(typ, addr, k)
			}
			v = cty.TupleVal(vals)
		}
	case e.mode == tfForEach:
		m := map[string]cty.Value{}
		for _, k := range e.keys {
			m[k.key] = in.instanceValue(typ, addr, k)
		}
		v = cty.ObjectVal(m)
	default:
		v = in.instanceValue(typ, addr, tfKey{})
	}
	in.res[addr] = v
	return v
}

func (in *tfInst) instanceValue(typ, addr string, k tfKey) cty.Value {
	id := in.prefix + addr + k.suffix()
	kind, ok := tfModelled[typ]
	if !ok {
		return tfUnknownVal("%s is a resource of type %s, which is not emulated", id, typ)
	}
	attrs := map[string]cty.Value{}
	for name := range attrNames[SourceTerraform][kind] {
		if name != "" {
			attrs[name] = in.ev.tfRefString(AttrRef{Stack: in.ev.src.Name, LogicalID: id, Attr: name})
		}
	}
	return cty.ObjectVal(attrs)
}
