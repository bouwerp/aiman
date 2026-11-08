package apicompat

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const schemaRefPrefix = "#/components/schemas/"

// enumLabels names the enums that carry the RPC surface, so a problem reads
// as "method ... removed" rather than as a bare enum change.
var enumLabels = map[string]string{
	"RpcRequest.method": "method",
	"StreamMethod":      "stream method",
	"SessionEvent.type": "event type",
	"RpcError.code":     "error code",
}

// CompareSpecs reports what newYAML breaks for a client of oldYAML: a removed
// path operation, schema, property or enum value (methods, stream methods,
// event types and error codes are enums), a property whose type or $ref
// changed, a request schema property that became required, and info.version
// moving backwards. Additions are compatible, including required fields of a
// new schema or of a result schema.
func CompareSpecs(oldYAML, newYAML []byte) []string {
	var oldDoc, newDoc map[string]any
	if err := yaml.Unmarshal(oldYAML, &oldDoc); err != nil {
		return []string{"previous spec: " + err.Error()}
	}
	if err := yaml.Unmarshal(newYAML, &newDoc); err != nil {
		return []string{"spec: " + err.Error()}
	}
	c := &specComparer{}
	c.version(oldDoc, newDoc)
	c.paths(asMap(oldDoc["paths"]), asMap(newDoc["paths"]))

	oldSchemas := asMap(asMap(oldDoc["components"])["schemas"])
	newSchemas := asMap(asMap(newDoc["components"])["schemas"])
	requests := requestSchemas(oldDoc)
	maps.Copy(requests, requestSchemas(newDoc))
	for _, name := range sortedKeys(oldSchemas) {
		cur, ok := newSchemas[name]
		if !ok {
			c.add("schema %s removed", name)
			continue
		}
		c.schema(name, asMap(oldSchemas[name]), asMap(cur), requests[name])
	}
	return c.problems
}

type specComparer struct{ problems []string }

func (c *specComparer) add(format string, args ...any) {
	c.problems = append(c.problems, fmt.Sprintf(format, args...))
}

func (c *specComparer) version(oldDoc, newDoc map[string]any) {
	oldV := fmt.Sprint(asMap(oldDoc["info"])["version"])
	newV := fmt.Sprint(asMap(newDoc["info"])["version"])
	if versionLess(newV, oldV) {
		c.add("info.version moved backwards: %s -> %s", oldV, newV)
	}
}

func (c *specComparer) paths(oldPaths, newPaths map[string]any) {
	for _, p := range sortedKeys(oldPaths) {
		cur, ok := newPaths[p]
		if !ok {
			c.add("path %s removed", p)
			continue
		}
		curOps := asMap(cur)
		for _, op := range sortedKeys(asMap(oldPaths[p])) {
			if _, ok := curOps[op]; !ok {
				c.add("operation %s %s removed", strings.ToUpper(op), p)
			}
		}
	}
}

// schema compares one schema node, recursing into properties, items,
// additionalProperties and the oneOf/anyOf/allOf branches. req marks a schema
// clients send, where a newly required property breaks old callers.
func (c *specComparer) schema(where string, oldS, newS map[string]any, req bool) {
	if oldT, ok := oldS["type"]; ok && typeString(oldT) != typeString(newS["type"]) {
		c.add("%s: type changed %s -> %s", where, typeString(oldT), typeString(newS["type"]))
	}
	if oldRef, ok := oldS["$ref"]; ok && fmt.Sprint(oldRef) != fmt.Sprint(newS["$ref"]) {
		c.add("%s: $ref changed %v -> %v", where, oldRef, newS["$ref"])
	}
	c.enum(where, oldS["enum"], newS["enum"])
	if req {
		c.required(where, oldS["required"], newS["required"])
	}

	oldProps, newProps := asMap(oldS["properties"]), asMap(newS["properties"])
	for _, p := range sortedKeys(oldProps) {
		cur, ok := newProps[p]
		if !ok {
			c.add("%s: property %s removed", where, p)
			continue
		}
		c.schema(where+"."+p, asMap(oldProps[p]), asMap(cur), req)
	}
	for _, key := range []string{"items", "additionalProperties"} {
		oldSub, ok := oldS[key].(map[string]any)
		if !ok {
			continue
		}
		newSub, ok := newS[key].(map[string]any)
		if !ok {
			c.add("%s: %s schema removed", where, key)
			continue
		}
		c.schema(where+"."+key, oldSub, newSub, req)
	}
	for _, key := range []string{"oneOf", "anyOf", "allOf"} {
		oldList, _ := oldS[key].([]any)
		newList, _ := newS[key].([]any)
		c.branches(where+"."+key, oldList, newList, req)
	}
}

// branches compares composition branches as a set: a $ref branch must still
// be listed wherever it now sits, and inline branches pair up in order among
// themselves, so inserting a branch never reads as a change to the others.
func (c *specComparer) branches(where string, oldList, newList []any, req bool) {
	refs := map[string]bool{}
	var inline []map[string]any
	for _, b := range newList {
		m := asMap(b)
		if ref, ok := m["$ref"]; ok {
			refs[fmt.Sprint(ref)] = true
		} else {
			inline = append(inline, m)
		}
	}
	i := 0
	for _, b := range oldList {
		m := asMap(b)
		if ref, ok := m["$ref"]; ok {
			if !refs[fmt.Sprint(ref)] {
				c.add("%s: branch %v removed", where, ref)
			}
			continue
		}
		if i >= len(inline) {
			c.add("%s: inline branch %d removed", where, i)
		} else {
			c.schema(fmt.Sprintf("%s[%d]", where, i), m, inline[i], req)
		}
		i++
	}
}

func (c *specComparer) enum(where string, oldEnum, newEnum any) {
	oldVals, _ := oldEnum.([]any)
	if len(oldVals) == 0 {
		return
	}
	newVals, _ := newEnum.([]any)
	have := map[string]bool{}
	for _, v := range newVals {
		have[fmt.Sprint(v)] = true
	}
	for _, v := range oldVals {
		s := fmt.Sprint(v)
		if have[s] {
			continue
		}
		if label, ok := enumLabels[where]; ok {
			c.add("%s %q removed (%s)", label, s, where)
		} else {
			c.add("%s: enum value %q removed", where, s)
		}
	}
}

func (c *specComparer) required(where string, oldReq, newReq any) {
	was := stringList(oldReq)
	for _, r := range stringList(newReq) {
		if !slices.Contains(was, r) {
			c.add("%s: request property %s is now required", where, r)
		}
	}
}

// requestSchemas names the schemas a client sends: those a requestBody
// references, everything they reference in turn, and any schema whose name
// ends in Request or Params.
func requestSchemas(doc map[string]any) map[string]bool {
	schemas := asMap(asMap(doc["components"])["schemas"])
	out := map[string]bool{}
	var queue []string
	for name := range schemas {
		if strings.HasSuffix(name, "Request") || strings.HasSuffix(name, "Params") {
			queue = append(queue, name)
		}
	}
	for _, item := range asMap(doc["paths"]) {
		for _, op := range asMap(item) {
			queue = append(queue, schemaRefs(asMap(op)["requestBody"])...)
		}
	}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if out[name] {
			continue
		}
		out[name] = true
		queue = append(queue, schemaRefs(schemas[name])...)
	}
	return out
}

// schemaRefs returns the component schema names referenced anywhere in node.
func schemaRefs(node any) []string {
	var out []string
	switch v := node.(type) {
	case map[string]any:
		if ref, ok := v["$ref"].(string); ok && strings.HasPrefix(ref, schemaRefPrefix) {
			out = append(out, strings.TrimPrefix(ref, schemaRefPrefix))
		}
		for _, sub := range v {
			out = append(out, schemaRefs(sub)...)
		}
	case []any:
		for _, sub := range v {
			out = append(out, schemaRefs(sub)...)
		}
	}
	return out
}

// typeString renders a schema type, a string or (OpenAPI 3.1) a list.
func typeString(t any) string {
	switch v := t.(type) {
	case nil:
		return "<none>"
	case []any:
		parts := make([]string, len(v))
		for i, p := range v {
			parts[i] = fmt.Sprint(p)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	return fmt.Sprint(t)
}

// versionLess reports a < b for dotted numeric versions; anything that does
// not parse compares as not less.
func versionLess(a, b string) bool {
	pa, okA := versionParts(a)
	pb, okB := versionParts(b)
	if !okA || !okB {
		return false
	}
	return slices.Compare(pa, pb) < 0
}

func versionParts(v string) ([]int, bool) {
	fields := strings.Split(strings.TrimPrefix(v, "v"), ".")
	out := make([]int, len(fields))
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func stringList(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, fmt.Sprint(item))
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
