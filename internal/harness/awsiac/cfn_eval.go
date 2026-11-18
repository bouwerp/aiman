package awsiac

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// cvKind is the shape of an evaluated template value.
type cvKind int

const (
	cvStr cvKind = iota
	cvNum
	cvBool
	cvNull
	cvList
	cvMap
	// cvNoValue is AWS::NoValue: the key or list item is removed.
	cvNoValue
	// cvUnres is a value aiman cannot evaluate; why says what.
	cvUnres
)

// cval is an evaluated template value. A string may hold references to
// planned resources (s); a number or a bool keeps its text in s. secret
// marks a value derived from a NoEcho parameter.
type cval struct {
	kind   cvKind
	s      Value
	list   []*cval
	keys   []string
	vals   []*cval
	why    string
	line   int
	secret bool
}

func unres(line int, format string, args ...any) *cval {
	return &cval{kind: cvUnres, why: fmt.Sprintf(format, args...), line: line}
}

func strVal(v Value, line int) *cval { return &cval{kind: cvStr, s: v, line: line} }

// field returns a map's value for key, or nil.
func (v *cval) field(key string) *cval {
	if v == nil || v.kind != cvMap {
		return nil
	}
	for i, k := range v.keys {
		if k == key {
			return v.vals[i]
		}
	}
	return nil
}

// value is a scalar's Value: strings with their references, numbers and
// bools as text.
func (v *cval) value() (Value, bool) {
	if v == nil {
		return nil, false
	}
	switch v.kind {
	case cvStr, cvNum, cvBool:
		return v.s, true
	}
	return nil, false
}

// literal is a scalar's text when it names no resource.
func (v *cval) literal() (string, bool) {
	val, ok := v.value()
	if !ok {
		return "", false
	}
	return val.Literal()
}

// firstUnres finds the first value inside v that cannot be evaluated.
func (v *cval) firstUnres() *cval {
	if v == nil {
		return nil
	}
	switch v.kind {
	case cvUnres:
		return v
	case cvList:
		for _, e := range v.list {
			if u := e.firstUnres(); u != nil {
				return u
			}
		}
	case cvMap:
		for _, e := range v.vals {
			if u := e.firstUnres(); u != nil {
				return u
			}
		}
	}
	return nil
}

// hasRefs reports whether a value names a planned resource anywhere.
func (v *cval) hasRefs() bool {
	if v == nil {
		return false
	}
	switch v.kind {
	case cvStr:
		return len(v.s.Refs()) > 0
	case cvList:
		return slices.ContainsFunc(v.list, (*cval).hasRefs)
	case cvMap:
		return slices.ContainsFunc(v.vals, (*cval).hasRefs)
	}
	return false
}

// isSecret reports whether a value derives from a NoEcho parameter.
func (v *cval) isSecret() bool {
	if v == nil {
		return false
	}
	if v.secret {
		return true
	}
	return slices.ContainsFunc(v.list, (*cval).isSecret) || slices.ContainsFunc(v.vals, (*cval).isSecret)
}

// tree turns an evaluated value into what JSON encodes: Values for
// strings, json.Number, bool, nil, slices and maps.
func (v *cval) tree() any {
	switch v.kind {
	case cvStr:
		return v.s
	case cvNum:
		s, _ := v.s.Literal()
		return json.Number(s)
	case cvBool:
		s, _ := v.s.Literal()
		return s == "true"
	case cvList:
		out := make([]any, len(v.list))
		for i, e := range v.list {
			out[i] = e.tree()
		}
		return out
	case cvMap:
		out := make(map[string]any, len(v.keys))
		for i, k := range v.keys {
			out[k] = v.vals[i].tree()
		}
		return out
	}
	return nil
}

// Pseudo parameters.
const (
	pseudoNoValue = "AWS::NoValue"
	stackIDSuffix = "/00000000-0000-0000-0000-000000000000"
)

// Transforms the importer applies.
const (
	samTransform      = "AWS::Serverless-2016-10-31"
	langExtTransform  = "AWS::LanguageExtensions"
	condInProgress    = 1
	condTrue          = 2
	condFalse         = 3
	condUnresolvable  = 4
	maxEvalCallsDepth = 256
)

// cfnStack evaluates one template.
type cfnStack struct {
	src  StackSource
	p    *Plan
	tmpl *cfnNode

	params   map[string]*cval
	conds    map[string]*cfnNode
	condMemo map[string]int
	condWhy  map[string]string
	mappings *cfnNode
	res      map[string]*cfnResource
	order    []string
	sam      bool
	langExt  bool
	depth    int
}

// cfnResource is one entry of the template's Resources.
type cfnResource struct {
	id    string
	typ   string
	node  *cfnNode
	props *cfnNode
	line  int
	// state is condTrue (included), condFalse (left out by its
	// condition) or condUnresolvable; why explains the last two.
	state int
	why   string
}

// supportedTypes are the types the importer plans as resources.
var supportedTypes = map[string]Kind{
	"AWS::DynamoDB::Table":         KindTable,
	"AWS::DynamoDB::GlobalTable":   KindTable,
	"AWS::Serverless::SimpleTable": KindTable,
	"AWS::S3::Bucket":              KindBucket,
	"AWS::SQS::Queue":              KindQueue,
	"AWS::SNS::Topic":              KindTopic,
	"AWS::SNS::Subscription":       KindSubscription,
}

func (s *cfnStack) pseudo(name string, line int) (*cval, bool) {
	region := s.p.Region
	switch name {
	case "AWS::Region":
		return strVal(Lit(region), line), true
	case "AWS::AccountId":
		return strVal(Lit(AccountID), line), true
	case "AWS::StackName":
		return strVal(Lit(s.src.Name), line), true
	case "AWS::StackId":
		return strVal(Lit("arn:aws:cloudformation:"+region+":"+AccountID+":stack/"+s.src.Name+stackIDSuffix), line), true
	case "AWS::Partition":
		return strVal(Lit("aws"), line), true
	case "AWS::URLSuffix":
		return strVal(Lit("amazonaws.com"), line), true
	case "AWS::NotificationARNs":
		return &cval{kind: cvList, line: line}, true
	case pseudoNoValue:
		return &cval{kind: cvNoValue, line: line}, true
	}
	return nil, false
}

// eval evaluates a template node.
func (s *cfnStack) eval(n *cfnNode) *cval {
	if n == nil {
		return &cval{kind: cvNull}
	}
	s.depth++
	defer func() { s.depth-- }()
	if s.depth > maxEvalCallsDepth {
		return unres(n.line, "the value nests too deeply to evaluate")
	}
	switch n.kind {
	case cfnScalar:
		switch n.scalar {
		case scalarNum:
			return &cval{kind: cvNum, s: Lit(n.text), line: n.line}
		case scalarBool:
			return &cval{kind: cvBool, s: Lit(n.text), line: n.line}
		case scalarNull:
			return &cval{kind: cvNull, line: n.line}
		}
		return strVal(Lit(n.text), n.line)
	case cfnSeq:
		out := &cval{kind: cvList, line: n.line}
		for _, it := range n.items {
			v := s.eval(it)
			if v.kind != cvNoValue {
				out.list = append(out.list, v)
			}
		}
		return out
	}
	if len(n.keys) == 1 && isIntrinsic(n.keys[0]) {
		return s.intrinsic(n.keys[0], n.vals[0], n.line)
	}
	out := &cval{kind: cvMap, line: n.line}
	for i, k := range n.keys {
		v := s.eval(n.vals[i])
		if v.kind != cvNoValue {
			out.keys = append(out.keys, k)
			out.vals = append(out.vals, v)
		}
	}
	return out
}

func isIntrinsic(k string) bool { return k == "Ref" || strings.HasPrefix(k, "Fn::") }

// args evaluates an intrinsic's argument list, which must have n entries
// (n < 0: any).
func (s *cfnStack) args(fn string, arg *cfnNode, n, line int) ([]*cfnNode, *cval) {
	if arg.kind != cfnSeq || (n >= 0 && len(arg.items) != n) {
		return nil, unres(line, "%s needs a list of %d arguments", fn, n)
	}
	return arg.items, nil
}

func (s *cfnStack) literalArg(fn string, n *cfnNode) (string, *cval) {
	v := s.eval(n)
	if u := v.firstUnres(); u != nil {
		return "", u
	}
	t, ok := v.literal()
	if !ok {
		return "", unres(n.line, "%s needs a literal argument here, not a value that names another resource", fn)
	}
	return t, nil
}

func (s *cfnStack) intrinsic(fn string, arg *cfnNode, line int) *cval {
	switch fn {
	case "Ref":
		name, u := s.literalArg(fn, arg)
		if u != nil {
			return u
		}
		return s.ref(name, line)
	case "Fn::GetAtt":
		return s.getAtt(arg, line)
	case "Fn::Sub":
		return s.fnSub(arg, line)
	case "Fn::Join":
		return s.fnJoin(arg, line)
	case "Fn::Select":
		return s.fnSelect(arg, line)
	case "Fn::Split":
		return s.fnSplit(arg, line)
	case "Fn::If":
		return s.fnIf(arg, line)
	case "Fn::Equals", "Fn::And", "Fn::Or", "Fn::Not":
		b, u := s.condExpr(&cfnNode{kind: cfnMap, keys: []string{fn}, vals: []*cfnNode{arg}, line: line})
		if u != nil {
			return u
		}
		return &cval{kind: cvBool, s: Lit(strconv.FormatBool(b)), line: line}
	case "Fn::FindInMap":
		return s.findInMap(arg, line)
	case "Fn::ImportValue":
		name, u := s.literalArg(fn, arg)
		if u != nil {
			return u
		}
		v, ok := s.src.Imports[name]
		if !ok {
			return unres(line, "Fn::ImportValue %s has no value in the stack's imports", name)
		}
		return strVal(Lit(v), line)
	case "Fn::GetAZs":
		region, u := s.literalArg(fn, arg)
		if u != nil {
			return u
		}
		if region == "" {
			region = s.p.Region
		}
		out := &cval{kind: cvList, line: line}
		for _, z := range []string{"a", "b", "c"} {
			out.list = append(out.list, strVal(Lit(region+z), line))
		}
		return out
	case "Fn::Base64":
		v := s.eval(arg)
		if u := v.firstUnres(); u != nil {
			return u
		}
		t, ok := v.literal()
		if !ok {
			return unres(line, "Fn::Base64 of a value that names another resource is known only once the plan is applied")
		}
		out := strVal(Lit(base64.StdEncoding.EncodeToString([]byte(t))), line)
		out.secret = v.isSecret()
		return out
	case "Fn::ToJsonString", "Fn::Length":
		if !s.langExt {
			return unres(line, "%s needs %s in the template's Transform", fn, langExtTransform)
		}
		v := s.eval(arg)
		if u := v.firstUnres(); u != nil {
			return u
		}
		if fn == "Fn::Length" {
			if v.kind != cvList {
				return unres(line, "Fn::Length needs a list")
			}
			return &cval{kind: cvNum, s: Lit(strconv.Itoa(len(v.list))), line: line}
		}
		j, err := JSON(v.tree())
		if err != nil {
			return unres(line, "Fn::ToJsonString: %v", err)
		}
		out := strVal(j, line)
		out.secret = v.isSecret()
		return out
	case "Fn::Transform":
		return unres(line, "Fn::Transform runs a macro, which aiman does not run")
	case "Fn::Cidr":
		return unres(line, "Fn::Cidr is not evaluated by aiman")
	}
	return unres(line, "%s is not a CloudFormation function aiman evaluates", fn)
}

// ref resolves Ref: a parameter, a pseudo parameter or a resource.
func (s *cfnStack) ref(name string, line int) *cval {
	if v, ok := s.params[name]; ok {
		return relined(v, line)
	}
	if v, ok := s.pseudo(name, line); ok {
		return v
	}
	return s.resourceRef(name, "", line)
}

// relined copies a parameter's value with the line of the reference.
func relined(v *cval, line int) *cval {
	c := *v
	c.line = line
	return &c
}

// resourceRef is a reference to a resource of the template: a planned
// type gives a Value that resolves after naming; anything else cannot be
// evaluated.
func (s *cfnStack) resourceRef(id, attr string, line int) *cval {
	r, ok := s.res[id]
	what := "Ref " + id
	if attr != "" {
		what = "Fn::GetAtt " + id + "." + attr
	}
	switch {
	case !ok:
		return unres(line, "%s: %s is not a parameter or resource of the template", what, id)
	case r.state == condFalse:
		return unres(line, "%s: %s is left out by its condition", what, id)
	case r.state == condUnresolvable:
		return unres(line, "%s: %s is skipped (%s)", what, id, r.why)
	}
	if _, ok := supportedTypes[r.typ]; !ok {
		return unres(line, "%s: %s is a %s, which is not emulated", what, id, r.typ)
	}
	return strVal(AttrOf(s.src.Name, id, attr), line)
}

func (s *cfnStack) getAtt(arg *cfnNode, line int) *cval {
	var id, attr string
	switch arg.kind {
	case cfnScalar:
		var ok bool
		id, attr, ok = strings.Cut(arg.text, ".")
		if !ok {
			return unres(line, "Fn::GetAtt %q needs the form Resource.Attribute", arg.text)
		}
	case cfnSeq:
		if len(arg.items) != 2 {
			return unres(line, "Fn::GetAtt needs [resource, attribute]")
		}
		var u *cval
		if id, u = s.literalArg("Fn::GetAtt", arg.items[0]); u != nil {
			return u
		}
		if attr, u = s.literalArg("Fn::GetAtt", arg.items[1]); u != nil {
			return u
		}
	default:
		return unres(line, "Fn::GetAtt needs [resource, attribute]")
	}
	if attr == "" {
		return unres(line, "Fn::GetAtt %s names no attribute", id)
	}
	return s.resourceRef(id, attr, line)
}

func (s *cfnStack) fnSub(arg *cfnNode, line int) *cval {
	tmpl := arg
	vars := map[string]*cval{}
	if arg.kind == cfnSeq {
		if len(arg.items) != 2 || arg.items[1].kind != cfnMap {
			return unres(line, "Fn::Sub needs a string or [string, {variables}]")
		}
		tmpl = arg.items[0]
		m := arg.items[1]
		for i, k := range m.keys {
			vars[k] = s.eval(m.vals[i])
		}
	}
	if tmpl.kind != cfnScalar {
		return unres(line, "Fn::Sub needs a literal template string")
	}
	return s.substitute(tmpl.text, vars, line)
}

// substitute expands ${Name}, ${Resource.Attribute} and ${!Literal}.
func (s *cfnStack) substitute(t string, vars map[string]*cval, line int) *cval {
	var out Value
	secret := false
	for {
		i := strings.Index(t, "${")
		if i < 0 {
			out = Concat(out, Lit(t))
			break
		}
		j := strings.IndexByte(t[i:], '}')
		if j < 0 {
			out = Concat(out, Lit(t))
			break
		}
		out = Concat(out, Lit(t[:i]))
		name := t[i+2 : i+j]
		t = t[i+j+1:]
		if strings.HasPrefix(name, "!") {
			out = Concat(out, Lit("${"+name[1:]+"}"))
			continue
		}
		name = strings.TrimSpace(name)
		var v *cval
		if vv, ok := vars[name]; ok {
			v = vv
		} else if id, attr, ok := strings.Cut(name, "."); ok && !strings.Contains(id, "::") {
			v = s.resourceRef(id, attr, line)
		} else {
			v = s.ref(name, line)
		}
		if u := v.firstUnres(); u != nil {
			return u
		}
		val, ok := v.value()
		if !ok {
			return unres(line, "Fn::Sub variable %s is not a string", name)
		}
		out = Concat(out, val)
		secret = secret || v.isSecret()
	}
	if out == nil {
		out = Lit("")
	}
	r := strVal(out, line)
	r.secret = secret
	return r
}

func (s *cfnStack) fnJoin(arg *cfnNode, line int) *cval {
	items, u := s.args("Fn::Join", arg, 2, line)
	if u != nil {
		return u
	}
	delim, u := s.literalArg("Fn::Join", items[0])
	if u != nil {
		return u
	}
	list := s.eval(items[1])
	if u := list.firstUnres(); u != nil {
		return u
	}
	if list.kind != cvList {
		return unres(line, "Fn::Join needs a list to join")
	}
	var out Value
	secret := false
	for i, e := range list.list {
		val, ok := e.value()
		if !ok {
			return unres(line, "Fn::Join item %d is not a string", i)
		}
		if i > 0 {
			out = Concat(out, Lit(delim))
		}
		out = Concat(out, val)
		secret = secret || e.isSecret()
	}
	if out == nil {
		out = Lit("")
	}
	r := strVal(out, line)
	r.secret = secret
	return r
}

func (s *cfnStack) fnSelect(arg *cfnNode, line int) *cval {
	items, u := s.args("Fn::Select", arg, 2, line)
	if u != nil {
		return u
	}
	is, u := s.literalArg("Fn::Select", items[0])
	if u != nil {
		return u
	}
	i, err := strconv.Atoi(is)
	if err != nil {
		return unres(line, "Fn::Select index %q is not a number", is)
	}
	list := s.eval(items[1])
	if u := list.firstUnres(); u != nil {
		return u
	}
	if list.kind != cvList {
		return unres(line, "Fn::Select needs a list")
	}
	if i < 0 || i >= len(list.list) {
		return unres(line, "Fn::Select index %d is outside a list of %d", i, len(list.list))
	}
	return list.list[i]
}

func (s *cfnStack) fnSplit(arg *cfnNode, line int) *cval {
	items, u := s.args("Fn::Split", arg, 2, line)
	if u != nil {
		return u
	}
	delim, u := s.literalArg("Fn::Split", items[0])
	if u != nil {
		return u
	}
	src := s.eval(items[1])
	if u := src.firstUnres(); u != nil {
		return u
	}
	t, ok := src.literal()
	if !ok {
		return unres(line, "Fn::Split of a value that names another resource is known only once names are assigned")
	}
	if delim == "" {
		return unres(line, "Fn::Split needs a delimiter")
	}
	out := &cval{kind: cvList, line: line}
	for _, part := range strings.Split(t, delim) {
		e := strVal(Lit(part), line)
		e.secret = src.isSecret()
		out.list = append(out.list, e)
	}
	return out
}

func (s *cfnStack) fnIf(arg *cfnNode, line int) *cval {
	items, u := s.args("Fn::If", arg, 3, line)
	if u != nil {
		return u
	}
	name, u := s.literalArg("Fn::If", items[0])
	if u != nil {
		return u
	}
	b, u := s.condition(name, line)
	if u != nil {
		return u
	}
	if b {
		return s.eval(items[1])
	}
	return s.eval(items[2])
}

func (s *cfnStack) findInMap(arg *cfnNode, line int) *cval {
	if arg.kind != cfnSeq || (len(arg.items) != 3 && len(arg.items) != 4) {
		return unres(line, "Fn::FindInMap needs [map, top-level key, second-level key]")
	}
	var keys [3]string
	for i := range keys {
		k, u := s.literalArg("Fn::FindInMap", arg.items[i])
		if u != nil {
			return u
		}
		keys[i] = k
	}
	n := s.mappings.get(keys[0]).get(keys[1]).get(keys[2])
	if n == nil {
		if len(arg.items) == 4 && s.langExt {
			if d := arg.items[3].get("DefaultValue"); d != nil {
				return s.eval(d)
			}
		}
		return unres(line, "Fn::FindInMap: Mappings has no %s.%s.%s", keys[0], keys[1], keys[2])
	}
	return relined(s.eval(n), line)
}

// condition evaluates a named condition once.
func (s *cfnStack) condition(name string, line int) (bool, *cval) {
	switch s.condMemo[name] {
	case condTrue:
		return true, nil
	case condFalse:
		return false, nil
	case condUnresolvable:
		return false, unres(line, "condition %s cannot be evaluated: %s", name, s.condWhy[name])
	case condInProgress:
		return false, unres(line, "condition %s refers to itself", name)
	}
	n, ok := s.conds[name]
	if !ok {
		return false, unres(line, "condition %s is not in the template's Conditions", name)
	}
	s.condMemo[name] = condInProgress
	b, u := s.condExpr(n)
	switch {
	case u != nil:
		s.condMemo[name], s.condWhy[name] = condUnresolvable, u.why
		return false, unres(line, "condition %s cannot be evaluated: %s", name, u.why)
	case b:
		s.condMemo[name] = condTrue
	default:
		s.condMemo[name] = condFalse
	}
	return b, nil
}

// condExpr evaluates a condition expression: Fn::Equals, Fn::And,
// Fn::Or, Fn::Not, {Condition: name} or a value that is a bool.
func (s *cfnStack) condExpr(n *cfnNode) (bool, *cval) {
	if n.kind == cfnMap && len(n.keys) == 1 {
		arg := n.vals[0]
		switch n.keys[0] {
		case "Condition":
			name, u := s.literalArg("Condition", arg)
			if u != nil {
				return false, u
			}
			return s.condition(name, n.line)
		case "Fn::Equals":
			items, u := s.args("Fn::Equals", arg, 2, n.line)
			if u != nil {
				return false, u
			}
			a, b := s.eval(items[0]), s.eval(items[1])
			if u := a.firstUnres(); u != nil {
				return false, u
			}
			if u := b.firstUnres(); u != nil {
				return false, u
			}
			at, aok := a.literal()
			bt, bok := b.literal()
			if !aok || !bok {
				return false, unres(n.line, "Fn::Equals compares a value that names another resource or is not a string")
			}
			return at == bt, nil
		case "Fn::And", "Fn::Or":
			items, u := s.args(n.keys[0], arg, -1, n.line)
			if u != nil {
				return false, u
			}
			if len(items) < 2 || len(items) > 10 {
				return false, unres(n.line, "%s needs 2 to 10 conditions", n.keys[0])
			}
			and := n.keys[0] == "Fn::And"
			for _, it := range items {
				b, u := s.condExpr(it)
				if u != nil {
					return false, u
				}
				if and && !b {
					return false, nil
				}
				if !and && b {
					return true, nil
				}
			}
			return and, nil
		case "Fn::Not":
			items, u := s.args("Fn::Not", arg, 1, n.line)
			if u != nil {
				return false, u
			}
			b, u := s.condExpr(items[0])
			return !b, u
		}
	}
	v := s.eval(n)
	if u := v.firstUnres(); u != nil {
		return false, u
	}
	t, ok := v.literal()
	if !ok || (t != "true" && t != "false") {
		return false, unres(n.line, "a condition must be true or false")
	}
	return t == "true", nil
}

// Parameter types that name something in an account: the default is not
// a value aiman can use.
func accountParamType(t string) bool {
	return strings.HasPrefix(t, "AWS::SSM::Parameter::") || strings.HasPrefix(t, "AWS::EC2::") || strings.HasPrefix(t, "List<AWS::EC2::")
}

// cdkParam reports CDK's bootstrap and asset parameters, which take their
// default even when their type names an SSM parameter.
func cdkParam(name string) bool {
	return name == "BootstrapVersion" || strings.HasPrefix(name, "AssetParameters")
}

// loadParams evaluates Parameters: an override (the stack's parameters
// merged with the scenario's), else the Default. A parameter with no value
// or one that breaks its constraints is an error; one whose type names
// something in an account (SSM, EC2) without an override cannot be
// evaluated where it is used.
func (s *cfnStack) loadParams() error {
	s.params = map[string]*cval{}
	decl := s.tmpl.get("Parameters")
	var errs []error
	if decl != nil && decl.kind != cfnMap {
		return errors.New("the template's Parameters section is not a mapping")
	}
	declared := map[string]bool{}
	if decl != nil {
		for i, name := range decl.keys {
			declared[name] = true
			n := decl.vals[i]
			if v, err := s.param(name, n); err != nil {
				errs = append(errs, err)
			} else {
				s.params[name] = v
			}
		}
	}
	for _, k := range sortedKeys(s.src.Parameters) {
		if !declared[k] {
			errs = append(errs, fmt.Errorf("parameter %s is not declared in the template", k))
		}
	}
	return errors.Join(errs...)
}

func scalarText(n *cfnNode) (string, bool) {
	if n == nil {
		return "", false
	}
	switch n.kind {
	case cfnScalar:
		return n.text, n.scalar != scalarNull
	case cfnSeq:
		var parts []string
		for _, it := range n.items {
			if it.kind != cfnScalar {
				return "", false
			}
			parts = append(parts, it.text)
		}
		return strings.Join(parts, ","), true
	}
	return "", false
}

func (s *cfnStack) param(name string, n *cfnNode) (*cval, error) {
	if n.kind != cfnMap {
		return nil, fmt.Errorf("parameter %s (line %d) is not a mapping", name, n.line)
	}
	typ, _ := scalarText(n.get("Type"))
	secret := false
	if t, _ := scalarText(n.get("NoEcho")); strings.EqualFold(t, "true") {
		secret = true
	}
	val, explicit := s.src.Parameters[name]
	if !explicit {
		if accountParamType(typ) && !cdkParam(name) {
			return &cval{kind: cvUnres, why: fmt.Sprintf("parameter %s has type %s; give it a value in the stack's parameters", name, typ), line: n.line}, nil
		}
		d, ok := scalarText(n.get("Default"))
		if !ok {
			return nil, fmt.Errorf("parameter %s (line %d) has no Default and no value in the stack's parameters", name, n.line)
		}
		val = d
	}
	shown := strconv.Quote(val)
	if secret {
		shown = "(NoEcho value)"
	}
	list := typ == "CommaDelimitedList" || strings.HasPrefix(typ, "List<")
	var items []string
	if list {
		for _, it := range strings.Split(val, ",") {
			items = append(items, strings.TrimSpace(it))
		}
	} else {
		items = []string{val}
	}
	for _, it := range items {
		err := checkParam(n, typ, it)
		var unchecked uncheckedPattern
		if errors.As(err, &unchecked) {
			s.p.Note(Warning{Stack: s.src.Name, Resource: "Parameters." + name, File: s.src.Path, Line: n.line, Message: unchecked.Error()})
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("parameter %s (line %d) value %s: %w", name, n.line, shown, err)
		}
	}
	if !list {
		return &cval{kind: cvStr, s: Lit(val), line: n.line, secret: secret}, nil
	}
	out := &cval{kind: cvList, line: n.line, secret: secret}
	for _, it := range items {
		out.list = append(out.list, &cval{kind: cvStr, s: Lit(it), line: n.line, secret: secret})
	}
	return out, nil
}

// checkParam checks one value against a parameter's constraints.
func checkParam(n *cfnNode, typ, v string) error {
	if typ == "Number" || typ == "List<Number>" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(f) {
			return errors.New("is not a number")
		}
		if t, ok := scalarText(n.get("MinValue")); ok {
			if m, err := strconv.ParseFloat(t, 64); err == nil && f < m {
				return fmt.Errorf("is below MinValue %s", t)
			}
		}
		if t, ok := scalarText(n.get("MaxValue")); ok {
			if m, err := strconv.ParseFloat(t, 64); err == nil && f > m {
				return fmt.Errorf("is above MaxValue %s", t)
			}
		}
	}
	if t, ok := scalarText(n.get("MinLength")); ok {
		if m, err := strconv.Atoi(t); err == nil && len(v) < m {
			return fmt.Errorf("is shorter than MinLength %s", t)
		}
	}
	if t, ok := scalarText(n.get("MaxLength")); ok {
		if m, err := strconv.Atoi(t); err == nil && len(v) > m {
			return fmt.Errorf("is longer than MaxLength %s", t)
		}
	}
	if av := n.get("AllowedValues"); av != nil && av.kind == cfnSeq {
		ok := false
		var allowed []string
		for _, it := range av.items {
			allowed = append(allowed, it.text)
			if it.text == v {
				ok = true
			}
		}
		if !ok {
			return fmt.Errorf("is not one of AllowedValues %s", strings.Join(allowed, ", "))
		}
	}
	if t, ok := scalarText(n.get("AllowedPattern")); ok {
		re, err := regexp.Compile("^(?:" + t + ")$")
		if err != nil {
			return uncheckedPattern(fmt.Sprintf("AllowedPattern %q is not a pattern aiman can check (%v); the value is not checked against it", t, err))
		}
		if !re.MatchString(v) {
			return fmt.Errorf("does not match AllowedPattern %s", t)
		}
	}
	return nil
}

// uncheckedPattern is an AllowedPattern Go's regexp cannot compile (a Java
// feature such as a lookahead): a note, not a refusal.
type uncheckedPattern string

func (u uncheckedPattern) Error() string { return string(u) }
