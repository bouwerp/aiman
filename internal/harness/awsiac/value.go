package awsiac

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Part is one piece of a Value: literal text, or a reference to a planned
// resource's attribute that resolves once names (and, for learned
// attributes, the emulator's answers) are known.
type Part struct {
	Lit string   `json:"lit,omitempty"`
	Ref *AttrRef `json:"ref,omitempty"`
}

// AttrRef names an attribute of a planned resource. Attr is the name the
// resource's source uses (CloudFormation Fn::GetAtt names such as Arn and
// StreamArn, Terraform attribute names such as arn and stream_arn); empty
// is the resource's Ref value (CloudFormation Ref, Terraform id).
type AttrRef struct {
	Stack     string `json:"stack"`
	LogicalID string `json:"logical_id"`
	Attr      string `json:"attr,omitempty"`
}

// Key is the planned resource the reference points at.
func (r AttrRef) Key() string { return Key(r.Stack, r.LogicalID) }

func (r AttrRef) String() string {
	if r.Attr == "" {
		return r.Key()
	}
	return r.Key() + "." + r.Attr
}

// Value is a string an importer could not finish on its own: literal parts
// and references, concatenated. A nil Value is unset.
type Value []Part

// Lit is a literal value.
func Lit(s string) Value { return Value{{Lit: s}} }

// RefOf is a resource's Ref value (CloudFormation Ref, Terraform id).
func RefOf(stack, logicalID string) Value { return AttrOf(stack, logicalID, "") }

// AttrOf is a resource's attribute, named as its source names it.
func AttrOf(stack, logicalID, attr string) Value {
	return Value{{Ref: &AttrRef{Stack: stack, LogicalID: logicalID, Attr: attr}}}
}

// Concat joins values, as Fn::Join, Fn::Sub or a Terraform template would.
func Concat(vs ...Value) Value {
	var out Value
	for _, v := range vs {
		for _, p := range v {
			if p.Ref == nil && len(out) > 0 && out[len(out)-1].Ref == nil {
				out[len(out)-1].Lit += p.Lit
				continue
			}
			out = append(out, p)
		}
	}
	return out
}

// Literal returns the value's text when it holds no reference.
func (v Value) Literal() (string, bool) {
	var b strings.Builder
	for _, p := range v {
		if p.Ref != nil {
			return "", false
		}
		b.WriteString(p.Lit)
	}
	return b.String(), true
}

// Refs lists the value's references.
func (v Value) Refs() []AttrRef {
	var out []AttrRef
	for _, p := range v {
		if p.Ref != nil {
			out = append(out, *p.Ref)
		}
	}
	return out
}

// String shows the value with references as ${stack.id.attr}.
func (v Value) String() string {
	var b strings.Builder
	for _, p := range v {
		if p.Ref != nil {
			b.WriteString("${" + p.Ref.String() + "}")
		} else {
			b.WriteString(p.Lit)
		}
	}
	return b.String()
}

// Placeholders that mark a reference inside JSON while it is encoded:
// private-use runes json.Marshal leaves as they are.
const (
	jsonRefOpen  = ''
	jsonRefClose = ''
)

// JSON encodes v, a tree of maps, slices, strings, numbers, bools and
// Values, as a JSON document Value: a policy or a redrive policy that names
// another resource's ARN. Literal text is escaped as JSON; references
// resolve to names, ARNs and URLs, which need no escaping.
func JSON(v any) (Value, error) {
	var refs []AttrRef
	tree, err := placeRefs(v, &refs)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(tree)
	if err != nil {
		return nil, err
	}
	var out Value
	s := string(b)
	for {
		i := strings.IndexRune(s, jsonRefOpen)
		if i < 0 {
			return Concat(out, Lit(s)), nil
		}
		j := strings.IndexRune(s[i:], jsonRefClose)
		if j < 0 {
			return nil, fmt.Errorf("awsiac: unbalanced reference marker in JSON")
		}
		n, err := strconv.Atoi(s[i+len(string(jsonRefOpen)) : i+j])
		if err != nil || n >= len(refs) {
			return nil, fmt.Errorf("awsiac: bad reference marker in JSON")
		}
		r := refs[n]
		out = Concat(out, Lit(s[:i]), Value{{Ref: &r}})
		s = s[i+j+len(string(jsonRefClose)):]
	}
}

func placeRefs(v any, refs *[]AttrRef) (any, error) {
	switch t := v.(type) {
	case Value:
		var b strings.Builder
		for _, p := range t {
			if strings.ContainsAny(p.Lit, string([]rune{jsonRefOpen, jsonRefClose})) {
				return nil, fmt.Errorf("awsiac: value holds a reserved character")
			}
			if p.Ref == nil {
				b.WriteString(p.Lit)
				continue
			}
			b.WriteRune(jsonRefOpen)
			b.WriteString(strconv.Itoa(len(*refs)))
			b.WriteRune(jsonRefClose)
			*refs = append(*refs, *p.Ref)
		}
		return b.String(), nil
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			x, err := placeRefs(e, refs)
			if err != nil {
				return nil, err
			}
			out[k] = x
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			x, err := placeRefs(e, refs)
			if err != nil {
				return nil, err
			}
			out[i] = x
		}
		return out, nil
	default:
		return v, nil
	}
}
