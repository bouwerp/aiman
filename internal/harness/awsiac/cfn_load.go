package awsiac

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"gopkg.in/yaml.v3"
)

// Template limits: a template larger than this, or nested deeper, is
// refused before it is evaluated.
const (
	MaxTemplateBytes = 1 << 20
	maxTemplateDepth = 64
)

// cfnKind is the shape of a template node.
type cfnKind int

const (
	cfnScalar cfnKind = iota
	cfnSeq
	cfnMap
)

// Scalar types a template node keeps from YAML or JSON.
const (
	scalarStr  = "str"
	scalarNum  = "num"
	scalarBool = "bool"
	scalarNull = "null"
)

// cfnNode is one node of a template: a scalar with its type, a sequence or
// a mapping with its keys in order. Line is the 1-based line it starts on.
// Short-form tags (!Ref, !Sub, …) are already rewritten to their long form
// ({"Ref": …}, {"Fn::Sub": …}).
type cfnNode struct {
	kind   cfnKind
	scalar string // scalarStr, scalarNum, scalarBool, scalarNull
	text   string
	items  []*cfnNode
	keys   []string
	vals   []*cfnNode
	line   int
}

// get returns a mapping's value for key, or nil.
func (n *cfnNode) get(key string) *cfnNode {
	if n == nil || n.kind != cfnMap {
		return nil
	}
	for i, k := range n.keys {
		if k == key {
			return n.vals[i]
		}
	}
	return nil
}

// set replaces or appends a mapping entry.
func (n *cfnNode) set(key string, v *cfnNode) {
	for i, k := range n.keys {
		if k == key {
			n.vals[i] = v
			return
		}
	}
	n.keys = append(n.keys, key)
	n.vals = append(n.vals, v)
}

func strNode(s string, line int) *cfnNode {
	return &cfnNode{kind: cfnScalar, scalar: scalarStr, text: s, line: line}
}

// shortTags maps CloudFormation's YAML short-form tags to the long form.
var shortTags = map[string]string{
	"!Ref":          "Ref",
	"!Condition":    "Condition",
	"!Sub":          "Fn::Sub",
	"!GetAtt":       "Fn::GetAtt",
	"!Join":         "Fn::Join",
	"!Select":       "Fn::Select",
	"!Split":        "Fn::Split",
	"!If":           "Fn::If",
	"!Equals":       "Fn::Equals",
	"!And":          "Fn::And",
	"!Or":           "Fn::Or",
	"!Not":          "Fn::Not",
	"!FindInMap":    "Fn::FindInMap",
	"!ImportValue":  "Fn::ImportValue",
	"!GetAZs":       "Fn::GetAZs",
	"!Base64":       "Fn::Base64",
	"!Cidr":         "Fn::Cidr",
	"!Transform":    "Fn::Transform",
	"!ToJsonString": "Fn::ToJsonString",
	"!Length":       "Fn::Length",
}

// readTemplate reads and parses a template from the tree: YAML (with
// short-form tags) or JSON. Anchors and aliases, duplicate keys, a file
// over MaxTemplateBytes and nesting deeper than 64 are refused.
func readTemplate(root fs.FS, name string) (*cfnNode, error) {
	fh, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	fi, err := fh.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	b, err := io.ReadAll(io.LimitReader(fh, MaxTemplateBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxTemplateBytes {
		return nil, fmt.Errorf("%s is over %d bytes", name, MaxTemplateBytes)
	}
	return parseTemplate(b)
}

// parseTemplate parses template bytes; a document starting with { is read
// as JSON first, so a JSON template keeps JSON's rules (tabs, escapes).
func parseTemplate(b []byte) (*cfnNode, error) {
	var n *cfnNode
	var err error
	if t := bytes.TrimLeft(b, " \t\r\n\ufeff"); len(t) > 0 && t[0] == '{' {
		n, err = parseJSONTemplate(b)
		var syn *json.SyntaxError
		if errors.As(err, &syn) {
			n, err = parseYAMLTemplate(b)
		}
	} else {
		n, err = parseYAMLTemplate(b)
	}
	if err != nil {
		return nil, err
	}
	if n == nil || n.kind != cfnMap {
		return nil, errors.New("the template is not a mapping")
	}
	return n, nil
}

func parseYAMLTemplate(b []byte) (*cfnNode, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("the template is empty")
		}
		return nil, err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, errors.New("the template holds more than one YAML document")
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, errors.New("the template is empty")
	}
	return fromYAML(doc.Content[0], 0)
}

func fromYAML(y *yaml.Node, depth int) (*cfnNode, error) {
	if depth > maxTemplateDepth {
		return nil, fmt.Errorf("line %d: the template nests deeper than %d", y.Line, maxTemplateDepth)
	}
	if y.Kind == yaml.AliasNode || y.Anchor != "" {
		return nil, fmt.Errorf("line %d: YAML anchors and aliases are not allowed in a template", y.Line)
	}
	var n *cfnNode
	switch y.Kind {
	case yaml.ScalarNode:
		n = &cfnNode{kind: cfnScalar, text: y.Value, line: y.Line}
		switch y.ShortTag() {
		case "!!int", "!!float":
			n.scalar = scalarNum
		case "!!bool":
			n.scalar = scalarBool
			n.text = strings.ToLower(y.Value)
		case "!!null":
			n.scalar, n.text = scalarNull, ""
		default:
			n.scalar = scalarStr
		}
	case yaml.SequenceNode:
		n = &cfnNode{kind: cfnSeq, line: y.Line}
		for _, c := range y.Content {
			cn, err := fromYAML(c, depth+1)
			if err != nil {
				return nil, err
			}
			n.items = append(n.items, cn)
		}
	case yaml.MappingNode:
		n = &cfnNode{kind: cfnMap, line: y.Line}
		seen := map[string]bool{}
		for i := 0; i+1 < len(y.Content); i += 2 {
			k := y.Content[i]
			if k.Kind != yaml.ScalarNode || k.Anchor != "" {
				return nil, fmt.Errorf("line %d: a mapping key must be a plain scalar", k.Line)
			}
			if k.Value == "<<" && k.ShortTag() == "!!merge" {
				return nil, fmt.Errorf("line %d: YAML merge keys are not allowed in a template", k.Line)
			}
			if seen[k.Value] {
				return nil, fmt.Errorf("line %d: duplicate key %q", k.Line, k.Value)
			}
			seen[k.Value] = true
			v, err := fromYAML(y.Content[i+1], depth+1)
			if err != nil {
				return nil, err
			}
			n.keys = append(n.keys, k.Value)
			n.vals = append(n.vals, v)
		}
	default:
		return nil, fmt.Errorf("line %d: unexpected YAML node", y.Line)
	}
	if tag := y.Tag; strings.HasPrefix(tag, "!") && !strings.HasPrefix(tag, "!!") {
		long, ok := shortTags[tag]
		if !ok {
			return nil, fmt.Errorf("line %d: unknown tag %s", y.Line, tag)
		}
		if n.kind == cfnScalar {
			n.scalar = scalarStr
		}
		return &cfnNode{kind: cfnMap, keys: []string{long}, vals: []*cfnNode{n}, line: y.Line}, nil
	}
	return n, nil
}

// parseJSONTemplate builds the tree from JSON tokens, keeping lines and
// refusing duplicate keys (encoding/json would keep the last silently).
func parseJSONTemplate(b []byte) (*cfnNode, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	n, err := jsonValue(dec, jsonLines{b: b, idx: newLineIndex(b)}, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("the template has data after its JSON document")
	}
	return n, nil
}

type lineIndex []int

func newLineIndex(b []byte) lineIndex {
	idx := lineIndex{0}
	for i, c := range b {
		if c == '\n' {
			idx = append(idx, i+1)
		}
	}
	return idx
}

// line is the 1-based line of the first non-space byte at or after off.
func (l lineIndex) line(b []byte, off int64) int {
	o := int(off)
	for o < len(b) && (b[o] == ' ' || b[o] == '\t' || b[o] == '\r' || b[o] == '\n' || b[o] == ',' || b[o] == ':') {
		o++
	}
	lo, hi := 0, len(l)
	for lo+1 < hi {
		mid := (lo + hi) / 2
		if l[mid] <= o {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo + 1
}

type jsonLines struct {
	b   []byte
	idx lineIndex
}

func jsonValue(dec *json.Decoder, l jsonLinesSource, depth int) (*cfnNode, error) {
	if depth > maxTemplateDepth {
		return nil, fmt.Errorf("the template nests deeper than %d", maxTemplateDepth)
	}
	line := l.at(dec.InputOffset())
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			n := &cfnNode{kind: cfnMap, line: line}
			seen := map[string]bool{}
			for dec.More() {
				kline := l.at(dec.InputOffset())
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, _ := kt.(string)
				if seen[k] {
					return nil, fmt.Errorf("line %d: duplicate key %q", kline, k)
				}
				seen[k] = true
				v, err := jsonValue(dec, l, depth+1)
				if err != nil {
					return nil, err
				}
				n.keys = append(n.keys, k)
				n.vals = append(n.vals, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return n, nil
		case '[':
			n := &cfnNode{kind: cfnSeq, line: line}
			for dec.More() {
				v, err := jsonValue(dec, l, depth+1)
				if err != nil {
					return nil, err
				}
				n.items = append(n.items, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return n, nil
		}
		return nil, fmt.Errorf("line %d: unexpected %v", line, t)
	case string:
		return strNode(t, line), nil
	case json.Number:
		return &cfnNode{kind: cfnScalar, scalar: scalarNum, text: t.String(), line: line}, nil
	case bool:
		return &cfnNode{kind: cfnScalar, scalar: scalarBool, text: fmt.Sprint(t), line: line}, nil
	case nil:
		return &cfnNode{kind: cfnScalar, scalar: scalarNull, line: line}, nil
	}
	return nil, fmt.Errorf("line %d: unexpected JSON token", line)
}

// jsonLinesSource turns a decoder offset into a line.
type jsonLinesSource interface{ at(off int64) int }

func (j jsonLines) at(off int64) int { return j.idx.line(j.b, off) }
