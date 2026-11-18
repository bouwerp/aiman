package awsiac

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/zclconf/go-cty/cty"
)

// Terraform import is a static read of a stack directory's .tf and .tf.json
// files: no terraform binary, no providers, no state and no network
// (docs/specs/harness-aws.md, Terraform). These bounds keep a hostile tree
// from costing serve much.
const (
	tfMaxFileBytes   = 1 << 20
	tfMaxTotalBytes  = 16 << 20
	tfMaxFiles       = 512
	tfMaxModuleDepth = 8
	tfMaxInstances   = 1000
	tfMaxResources   = 5000
)

func init() { RegisterImporter(SourceTerraform, TerraformImporter) }

// TerraformImporter reads src.Path, a directory of Terraform files in
// src.Root, into the plan. A file it cannot parse, a variable with no value,
// a local module it cannot read and a reference cycle are errors; a value
// it cannot know statically is a warning naming the file and line.
func TerraformImporter(ctx context.Context, src StackSource, p *Plan) error {
	if src.Root == nil {
		return errors.New("terraform: no repository tree")
	}
	dir := path.Clean(src.Path)
	if !fs.ValidPath(dir) {
		return fmt.Errorf("terraform: %q is not a path inside the repository", src.Path)
	}
	l := &tfLoader{root: src.Root, parser: hclparse.NewParser(), modules: map[string]*tfModule{}}
	mod, err := l.module(dir)
	if err != nil {
		return err
	}
	ev := newTFEval(ctx, src, p, l)
	vars, err := ev.rootVars(mod)
	if err != nil {
		return err
	}
	ev.run(mod, vars)
	return errors.Join(ev.errs...)
}

// tfModule is one directory of Terraform files, parsed.
type tfModule struct {
	dir       string
	variables []*tfVariable
	locals    map[string]*hcl.Attribute
	resources []*tfResource
	data      []*tfResource
	calls     []*tfCall
	outputs   []*tfOutput
	// ignored lists override files, which aiman does not merge.
	ignored []string
}

func (m *tfModule) variable(name string) *tfVariable {
	for _, v := range m.variables {
		if v.name == name {
			return v
		}
	}
	return nil
}

func (m *tfModule) resource(typ, name string) *tfResource {
	for _, r := range m.resources {
		if r.typ == typ && r.name == name {
			return r
		}
	}
	return nil
}

func (m *tfModule) call(name string) *tfCall {
	for _, c := range m.calls {
		if c.name == name {
			return c
		}
	}
	return nil
}

func (m *tfModule) hasResourceType(typ string) bool {
	for _, r := range m.resources {
		if r.typ == typ {
			return true
		}
	}
	return false
}

// tfVariable is a variable block. def is cty.NilVal without a default.
type tfVariable struct {
	name        string
	def         cty.Value
	ty          cty.Type
	defaults    *typeexpr.Defaults
	typed       bool
	sensitive   bool
	validations int
	rng         hcl.Range
}

// tfResource is a resource (or data) block with its meta-arguments taken
// out of body.
type tfResource struct {
	typ, name string
	body      hcl.Body
	count     hcl.Expression
	forEach   hcl.Expression
	dependsOn []hcl.Traversal
	rng       hcl.Range
}

func (r *tfResource) addr() string { return r.typ + "." + r.name }

// tfCall is a module block: its source and the input expressions.
type tfCall struct {
	name    string
	source  string
	args    hcl.Attributes
	count   hcl.Expression
	forEach hcl.Expression
	rng     hcl.Range
}

// tfOutput is an output block.
type tfOutput struct {
	name      string
	expr      hcl.Expression
	sensitive bool
	rng       hcl.Range
}

// tfLoader parses module directories once each.
type tfLoader struct {
	root    fs.FS
	parser  *hclparse.Parser
	modules map[string]*tfModule
	total   int64
	files   int
}

var tfFileSchema = &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{
	{Type: "terraform"},
	{Type: "provider", LabelNames: []string{"name"}},
	{Type: "variable", LabelNames: []string{"name"}},
	{Type: "locals"},
	{Type: "output", LabelNames: []string{"name"}},
	{Type: "module", LabelNames: []string{"name"}},
	{Type: "resource", LabelNames: []string{"type", "name"}},
	{Type: "data", LabelNames: []string{"type", "name"}},
	{Type: "moved"},
	{Type: "import"},
	{Type: "removed"},
	{Type: "check", LabelNames: []string{"name"}},
}}

var tfVariableSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{{Name: "default"}, {Name: "type"}, {Name: "description"}, {Name: "sensitive"}, {Name: "nullable"}, {Name: "ephemeral"}},
	Blocks:     []hcl.BlockHeaderSchema{{Type: "validation"}},
}

var tfResourceMeta = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{{Name: "count"}, {Name: "for_each"}, {Name: "depends_on"}, {Name: "provider"}},
	Blocks:     []hcl.BlockHeaderSchema{{Type: "lifecycle"}, {Type: "provisioner", LabelNames: []string{"type"}}, {Type: "connection"}},
}

var tfOutputSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{{Name: "value", Required: true}, {Name: "sensitive"}, {Name: "description"}, {Name: "depends_on"}, {Name: "ephemeral"}},
	Blocks:     []hcl.BlockHeaderSchema{{Type: "precondition"}},
}

// tfIsConfigFile says whether a directory entry is a configuration file
// and whether it is an override file.
func tfIsConfigFile(name string) (ok, override bool) {
	base, isTF := strings.CutSuffix(name, ".tf")
	if !isTF {
		base, isTF = strings.CutSuffix(name, ".tf.json")
	}
	if !isTF || strings.HasPrefix(name, ".") {
		return false, false
	}
	return true, base == "override" || strings.HasSuffix(base, "_override")
}

// module parses the directory dir of the repository tree.
func (l *tfLoader) module(dir string) (*tfModule, error) {
	if m, ok := l.modules[dir]; ok {
		return m, nil
	}
	entries, err := fs.ReadDir(l.root, dir)
	if err != nil {
		return nil, fmt.Errorf("terraform: %w", err)
	}
	m := &tfModule{dir: dir, locals: map[string]*hcl.Attribute{}}
	var errs []error
	n := 0
	for _, e := range entries {
		ok, override := tfIsConfigFile(e.Name())
		if !ok || !e.Type().IsRegular() {
			continue
		}
		name := path.Join(dir, e.Name())
		if override {
			m.ignored = append(m.ignored, name)
			continue
		}
		f, err := l.parse(name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		n++
		if err := m.add(f.Body); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("terraform: %s holds no .tf or .tf.json file", dir)
	}
	l.modules[dir] = m
	return m, nil
}

// parse reads and parses one file, HCL or JSON by its name.
func (l *tfLoader) parse(name string) (*hcl.File, error) {
	b, err := l.read(name, tfMaxFileBytes)
	if err != nil {
		return nil, err
	}
	var f *hcl.File
	var diags hcl.Diagnostics
	if strings.HasSuffix(name, ".json") {
		f, diags = l.parser.ParseJSON(b, name)
	} else {
		f, diags = l.parser.ParseHCL(b, name)
	}
	if diags.HasErrors() {
		return nil, fmt.Errorf("terraform: %s", tfDiagText(diags))
	}
	return f, nil
}

// read reads a regular file of the tree, within the per-file and total
// bounds.
func (l *tfLoader) read(name string, limit int64) ([]byte, error) {
	if l.files++; l.files > tfMaxFiles {
		return nil, fmt.Errorf("terraform: more than %d files", tfMaxFiles)
	}
	fh, err := l.root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("terraform: %w", err)
	}
	defer fh.Close()
	fi, err := fh.Stat()
	if err != nil {
		return nil, fmt.Errorf("terraform: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("terraform: %s is not a regular file", name)
	}
	b, err := io.ReadAll(io.LimitReader(fh, limit+1))
	if err != nil {
		return nil, fmt.Errorf("terraform: %s: %w", name, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("terraform: %s is over %d bytes", name, limit)
	}
	if l.total += int64(len(b)); l.total > tfMaxTotalBytes {
		return nil, fmt.Errorf("terraform: the configuration is over %d bytes", tfMaxTotalBytes)
	}
	return b, nil
}

// add reads one file's top-level blocks into the module.
func (m *tfModule) add(body hcl.Body) error {
	content, _, diags := body.PartialContent(tfFileSchema)
	if diags.HasErrors() {
		return fmt.Errorf("terraform: %s", tfDiagText(diags))
	}
	var errs []error
	for _, b := range content.Blocks {
		var err error
		switch b.Type {
		case "variable":
			err = m.addVariable(b)
		case "locals":
			err = m.addLocals(b)
		case "resource", "data":
			err = m.addResource(b)
		case "module":
			err = m.addCall(b)
		case "output":
			err = m.addOutput(b)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *tfModule) addVariable(b *hcl.Block) error {
	content, _, diags := b.Body.PartialContent(tfVariableSchema)
	if diags.HasErrors() {
		return fmt.Errorf("terraform: %s", tfDiagText(diags))
	}
	v := &tfVariable{name: b.Labels[0], ty: cty.DynamicPseudoType, rng: b.DefRange}
	if m.variable(v.name) != nil {
		return fmt.Errorf("terraform: variable %q is declared twice (%s)", v.name, tfAt(b.DefRange))
	}
	if a, ok := content.Attributes["type"]; ok {
		ty, defs, diags := typeexpr.TypeConstraintWithDefaults(a.Expr)
		if diags.HasErrors() {
			return fmt.Errorf("terraform: variable %s: %s", v.name, tfDiagText(diags))
		}
		v.ty, v.defaults, v.typed = ty, defs, true
	}
	if a, ok := content.Attributes["default"]; ok {
		val, diags := a.Expr.Value(nil)
		if diags.HasErrors() {
			return fmt.Errorf("terraform: variable %s default: %s", v.name, tfDiagText(diags))
		}
		v.def = val
	}
	if a, ok := content.Attributes["sensitive"]; ok {
		val, diags := a.Expr.Value(nil)
		v.sensitive = !diags.HasErrors() && val.Type() == cty.Bool && val.IsKnown() && !val.IsNull() && val.True()
	}
	for _, blk := range content.Blocks {
		if blk.Type == "validation" {
			v.validations++
		}
	}
	m.variables = append(m.variables, v)
	return nil
}

func (m *tfModule) addLocals(b *hcl.Block) error {
	attrs, diags := b.Body.JustAttributes()
	if diags.HasErrors() {
		return fmt.Errorf("terraform: %s", tfDiagText(diags))
	}
	for name, a := range attrs {
		if _, dup := m.locals[name]; dup {
			return fmt.Errorf("terraform: local %q is declared twice (%s)", name, tfAt(a.NameRange))
		}
		m.locals[name] = a
	}
	return nil
}

func (m *tfModule) addResource(b *hcl.Block) error {
	content, rest, diags := b.Body.PartialContent(tfResourceMeta)
	if diags.HasErrors() {
		return fmt.Errorf("terraform: %s", tfDiagText(diags))
	}
	r := &tfResource{typ: b.Labels[0], name: b.Labels[1], body: rest, rng: b.DefRange}
	if a, ok := content.Attributes["count"]; ok {
		r.count = a.Expr
	}
	if a, ok := content.Attributes["for_each"]; ok {
		r.forEach = a.Expr
	}
	if r.count != nil && r.forEach != nil {
		return fmt.Errorf("terraform: %s sets both count and for_each (%s)", r.addr(), tfAt(b.DefRange))
	}
	if a, ok := content.Attributes["depends_on"]; ok {
		r.dependsOn = tfTraversals(a.Expr)
	}
	if b.Type == "data" {
		m.data = append(m.data, r)
		return nil
	}
	if m.resource(r.typ, r.name) != nil {
		return fmt.Errorf("terraform: %s is declared twice (%s)", r.addr(), tfAt(b.DefRange))
	}
	m.resources = append(m.resources, r)
	return nil
}

// tfTraversals reads a depends_on list of plain references.
func tfTraversals(expr hcl.Expression) []hcl.Traversal {
	exprs, diags := hcl.ExprList(expr)
	if diags.HasErrors() {
		return nil
	}
	var out []hcl.Traversal
	for _, e := range exprs {
		if t, diags := hcl.AbsTraversalForExpr(e); !diags.HasErrors() {
			out = append(out, t)
		}
	}
	return out
}

func (m *tfModule) addCall(b *hcl.Block) error {
	attrs, diags := b.Body.JustAttributes()
	if diags.HasErrors() {
		return fmt.Errorf("terraform: module %s: %s", b.Labels[0], tfDiagText(diags))
	}
	c := &tfCall{name: b.Labels[0], args: hcl.Attributes{}, rng: b.DefRange}
	if m.call(c.name) != nil {
		return fmt.Errorf("terraform: module %q is declared twice (%s)", c.name, tfAt(b.DefRange))
	}
	for name, a := range attrs {
		switch name {
		case "source":
			v, diags := a.Expr.Value(nil)
			if diags.HasErrors() || v.Type() != cty.String || v.IsNull() {
				return fmt.Errorf("terraform: module %s: source must be a literal string (%s)", c.name, tfAt(a.Range))
			}
			c.source = v.AsString()
		case "count":
			c.count = a.Expr
		case "for_each":
			c.forEach = a.Expr
		case "version", "providers", "depends_on":
		default:
			c.args[name] = a
		}
	}
	if c.source == "" {
		return fmt.Errorf("terraform: module %s has no source (%s)", c.name, tfAt(b.DefRange))
	}
	m.calls = append(m.calls, c)
	return nil
}

func (m *tfModule) addOutput(b *hcl.Block) error {
	content, _, diags := b.Body.PartialContent(tfOutputSchema)
	if diags.HasErrors() {
		return fmt.Errorf("terraform: output %s: %s", b.Labels[0], tfDiagText(diags))
	}
	o := &tfOutput{name: b.Labels[0], expr: content.Attributes["value"].Expr, rng: b.DefRange}
	if a, ok := content.Attributes["sensitive"]; ok {
		val, diags := a.Expr.Value(nil)
		o.sensitive = !diags.HasErrors() && val.Type() == cty.Bool && val.IsKnown() && !val.IsNull() && val.True()
	}
	m.outputs = append(m.outputs, o)
	return nil
}

// tfVarFile reads a .tfvars or .tfvars.json file: literal values only.
func (l *tfLoader) varFile(name string) (map[string]cty.Value, []string, error) {
	f, err := l.parse(name)
	if err != nil {
		return nil, nil, err
	}
	attrs, diags := f.Body.JustAttributes()
	if diags.HasErrors() {
		return nil, nil, fmt.Errorf("terraform: %s", tfDiagText(diags))
	}
	out := map[string]cty.Value{}
	for name, a := range attrs {
		v, diags := a.Expr.Value(nil)
		if diags.HasErrors() {
			return nil, nil, fmt.Errorf("terraform: %s", tfDiagText(diags))
		}
		out[name] = v
	}
	names := make([]string, 0, len(out))
	for k := range out {
		names = append(names, k)
	}
	sort.Strings(names)
	return out, names, nil
}

// autoVarFiles lists a stack directory's terraform.tfvars,
// terraform.tfvars.json and *.auto.tfvars(.json), in Terraform's order.
func (l *tfLoader) autoVarFiles(dir string) ([]string, error) {
	entries, err := fs.ReadDir(l.root, dir)
	if err != nil {
		return nil, fmt.Errorf("terraform: %w", err)
	}
	var out, auto []string
	for _, base := range []string{"terraform.tfvars", "terraform.tfvars.json"} {
		for _, e := range entries {
			if e.Name() == base && e.Type().IsRegular() {
				out = append(out, path.Join(dir, base))
			}
		}
	}
	for _, e := range entries {
		n := e.Name()
		if e.Type().IsRegular() && (strings.HasSuffix(n, ".auto.tfvars") || strings.HasSuffix(n, ".auto.tfvars.json")) {
			auto = append(auto, path.Join(dir, n))
		}
	}
	sort.Strings(auto)
	return append(out, auto...), nil
}

// tfDiagText is the first error diagnostic as one line, with its place.
func tfDiagText(diags hcl.Diagnostics) string {
	for _, d := range diags {
		if d.Severity != hcl.DiagError {
			continue
		}
		s := d.Summary
		if d.Detail != "" {
			s += ": " + d.Detail
		}
		if d.Subject != nil {
			s += " (" + tfAt(*d.Subject) + ")"
		}
		return s
	}
	return diags.Error()
}

func tfAt(r hcl.Range) string { return fmt.Sprintf("%s:%d", r.Filename, r.Start.Line) }
