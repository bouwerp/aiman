package awsiac

import (
	"net/url"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"
)

// A reference marker stands in for a name, URL or ARN that exists only
// once the plan is built, so anything that would look inside it (a
// comparison, a case change, a length, a split) must not run on the marker
// text: it would compute an answer for the marker, not for the name. The
// guards below make those operations an unknown that names the reference
// instead, and the usual skip or drop rules warn. They find markers by
// their runes, because HCL strips marks before it calls an operator.

// tfPassThrough are functions that only carry their arguments into the
// result whole (concatenation, collection building, JSON), by argument
// position; nil means every argument. The marker survives intact, so a
// reference in those arguments is safe. Any other function, or another
// argument of these, reading a reference is unresolved.
var tfPassThrough = map[string][]int{
	"format":     nil,
	"formatlist": nil,
	"join":       nil,
	"concat":     nil,
	"merge":      nil,
	"jsonencode": nil,
	"jsondecode": nil,
	"tostring":   nil,
	"tolist":     nil,
	"tomap":      nil,
	"flatten":    nil,
	"keys":       nil,
	"values":     nil,
	"coalesce":   nil,
	"try":        nil,
	"can":        nil,
	// The collection and the default pass through; the index or key
	// decides which element is taken.
	"element": {0},
	"lookup":  {0, 2},
}

func init() {
	for name, f := range tfFunctions {
		safe, pass := tfPassThrough[name]
		if pass && safe == nil {
			continue
		}
		tfFunctions[name] = tfGuard(name+"()", f, safe)
	}
	// length reads a string's characters, but only counts a collection's
	// elements, so a reference inside a collection is fine.
	tfFunctions["length"] = tfGuardShallow("length()", tfLengthFunc)
	// HCL's == and != are package operations shared by every expression;
	// only this package makes tfRef marks, so the guard changes nothing
	// for any other caller of hclsyntax in the binary.
	hclsyntax.OpEqual.Impl = tfGuard("==", stdlib.EqualFunc, nil)
	hclsyntax.OpNotEqual.Impl = tfGuard("!=", stdlib.NotEqualFunc, nil)
}

// tfGuard wraps f so that a reference marker in an argument outside safe
// makes the result an unknown naming it, instead of a value computed from
// the marker text.
func tfGuard(name string, f function.Function, safe []int) function.Function {
	return tfGuardWith(name, f, func(args []cty.Value) string { return tfRefIn(args, safe, true) })
}

// tfGuardShallow guards only arguments that are themselves marked.
func tfGuardShallow(name string, f function.Function) function.Function {
	return tfGuardWith(name, f, func(args []cty.Value) string { return tfRefIn(args, nil, false) })
}

func tfGuardWith(name string, f function.Function, refIn func([]cty.Value) string) function.Function {
	params := f.Params()
	for i := range params {
		params[i].AllowMarked, params[i].AllowUnknown, params[i].AllowDynamicType = true, true, true
	}
	var vp *function.Parameter
	if v := f.VarParam(); v != nil {
		c := *v
		c.AllowMarked, c.AllowUnknown, c.AllowDynamicType = true, true, true
		vp = &c
	}
	return function.New(&function.Spec{
		Description: f.Description(),
		Params:      params,
		VarParam:    vp,
		Type: func(args []cty.Value) (cty.Type, error) {
			if refIn(args) != "" {
				return cty.DynamicPseudoType, nil
			}
			return f.ReturnTypeForValues(args)
		},
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			if r := refIn(args); r != "" {
				return tfUnknownVal("%s reads %s, which aiman knows only by reference until the plan is applied", name, r), nil
			}
			v, err := f.Call(args)
			if err != nil {
				return v, err
			}
			// A function that allows marks itself can drop them on an
			// unknown result; keep why a value is unknown or sensitive.
			for _, a := range args {
				_, marks := a.UnmarkDeep()
				v = v.WithMarks(marks)
			}
			return v, nil
		},
	})
}

// tfRefIn names the first reference marker in a known string argument
// outside safe, anywhere inside it when deep, else only when the argument
// is itself a string.
func tfRefIn(args []cty.Value, safe []int, deep bool) string {
	var refs []string
	for i, a := range args {
		if tfSafeArg(i, safe) {
			continue
		}
		a, _ = a.UnmarkDeep()
		if !deep {
			if r := tfRefText(a); r != "" {
				refs = append(refs, r)
			}
			continue
		}
		_ = cty.Walk(a, func(_ cty.Path, v cty.Value) (bool, error) {
			if r := tfRefText(v); r != "" {
				refs = append(refs, r)
			}
			return true, nil
		})
	}
	if len(refs) == 0 {
		return ""
	}
	sort.Strings(refs)
	return refs[0]
}

// tfRefText is the first reference marked in a known string, or "".
func tfRefText(v cty.Value) string {
	if v.Type() != cty.String || !v.IsKnown() || v.IsNull() {
		return ""
	}
	s := v.AsString()
	i := strings.IndexRune(s, tfRefOpen)
	if i < 0 {
		if strings.ContainsRune(s, tfRefClose) {
			return "a cut resource reference"
		}
		return ""
	}
	s = s[i+len(string(tfRefOpen)):]
	if j := strings.IndexRune(s, tfRefClose); j >= 0 {
		if t, err := url.PathUnescape(s[:j]); err == nil {
			return t
		}
		return s[:j]
	}
	return "a cut resource reference"
}

func tfSafeArg(i int, safe []int) bool {
	for _, s := range safe {
		if s == i {
			return true
		}
	}
	return false
}

// tfRefString is a reference marker string.
func (ev *tfEval) tfRefString(r AttrRef) cty.Value { return cty.StringVal(ev.marker(r)) }
