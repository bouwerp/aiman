//go:build acceptance

package server

import (
	"slices"
	"strings"
	"testing"
)

// TestOpenAPIHasAWS checks the phone contract for the aws.* methods, their two
// result types and their refusal codes.
func TestOpenAPIHasAWS(t *testing.T) {
	spec := loadOpenAPI(t)

	methods := specEnum(t, spec, "RpcRequest", "properties", "method")
	for _, m := range []string{"aws.status", "aws.login_start", "aws.login_status"} {
		if !slices.Contains(methods, m) {
			t.Errorf("RpcRequest.method lacks %s", m)
		}
	}

	codes := specEnum(t, spec, "RpcError", "properties", "code")
	for _, c := range []string{"aws_no_sso", "aws_rate_limited", "not_found", "needs_operator", "unavailable"} {
		if !slices.Contains(codes, c) {
			t.Errorf("RpcError.code lacks %s", c)
		}
	}

	results := resultSchemasByType(t, spec)

	status, ok := results["aws_status"]
	if !ok {
		t.Fatalf("RpcResponse.result has no schema with type const aws_status")
	}
	item := mapAt(spec, mapAt(spec, mapAt(spec, status, "properties", "profiles"), "items"))
	assertProps(t, "aws_status.profiles[]", item,
		"profile", "status", "expires_at", "expiry_approx", "sso_ready")

	login, ok := results["aws_login"]
	if !ok {
		t.Fatalf("RpcResponse.result has no schema with type const aws_login")
	}
	loginObj := mapAt(spec, mapAt(spec, login, "properties", "login"))
	assertProps(t, "aws_login.login", loginObj,
		"login_id", "profile", "state", "failure", "expires_at",
		"user_code", "verification_uri_complete", "expires_in")
	if req := strSlice(loginObj["required"]); slices.Contains(req, "user_code") || slices.Contains(req, "verification_uri_complete") {
		t.Errorf("aws_login.login requires user_code or verification_uri_complete, which aws.login_status never returns")
	}
}

// resultSchemasByType maps each RpcResponse.result variant's type const to its
// resolved schema.
func resultSchemasByType(t *testing.T, spec map[string]any) map[string]map[string]any {
	t.Helper()
	resp := mapAt(spec, spec, "components", "schemas", "RpcResponse", "properties", "result")
	variants, _ := resp["oneOf"].([]any)
	if len(variants) == 0 {
		t.Fatalf("RpcResponse.result has no oneOf")
	}
	out := map[string]map[string]any{}
	for _, v := range variants {
		m := mapAt(spec, v)
		typ := mapAt(spec, m, "properties", "type")
		if c, ok := typ["const"].(string); ok {
			out[c] = m
		}
	}
	return out
}

// mapAt walks keys from node, resolving local $ref at each step.
func mapAt(spec map[string]any, node any, keys ...string) map[string]any {
	cur := resolveRef(spec, node)
	for _, k := range keys {
		if cur == nil {
			return nil
		}
		cur = resolveRef(spec, cur[k])
	}
	return cur
}

func resolveRef(spec map[string]any, node any) map[string]any {
	m, _ := node.(map[string]any)
	ref, ok := m["$ref"].(string)
	if !ok {
		return m
	}
	parts := strings.Split(strings.TrimPrefix(ref, "#/"), "/")
	var cur any = spec
	for _, p := range parts {
		cm, _ := cur.(map[string]any)
		cur = cm[p]
	}
	return resolveRef(spec, cur)
}

func assertProps(t *testing.T, what string, schema map[string]any, names ...string) {
	t.Helper()
	props, _ := schema["properties"].(map[string]any)
	for _, n := range names {
		if _, ok := props[n]; !ok {
			t.Errorf("%s lacks property %s", what, n)
		}
	}
}

func strSlice(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
