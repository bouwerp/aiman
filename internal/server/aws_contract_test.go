package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// TestAWSMethodsDispatched fails while serve answers an aws.* method with the
// unknown-method refusal. Before the handlers land, unavailable is enough.
func TestAWSMethodsDispatched(t *testing.T) {
	srv := New(nil, testRepo(t), nil, nil, nil, nil, "test")
	calls := map[string]map[string]any{
		"aws.status":       {},
		"aws.login_start":  {"profile": "dev"},
		"aws.login_status": {"login_id": "0123456789abcdef0123456789abcdef"},
	}
	for method, params := range calls {
		resp := rpc(t, srv, "", method, params)
		if resp.Error == nil {
			continue
		}
		if strings.Contains(resp.Error.Message, "unknown method") {
			t.Errorf("%s: dispatch has no case for it: %s %q", method, resp.Error.Code, resp.Error.Message)
		}
		if resp.Error.Code == "" {
			t.Errorf("%s: error with no code: %q", method, resp.Error.Message)
		}
	}
}

// TestAWSErrorCodesDefined reads protocol.go's constants from source, so it
// compiles before they exist.
func TestAWSErrorCodesDefined(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "protocol.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	consts := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range vs.Names {
			if i >= len(vs.Values) {
				continue
			}
			lit, ok := vs.Values[i].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			v, err := strconv.Unquote(lit.Value)
			if err == nil {
				consts[name.Name] = v
			}
		}
		return true
	})
	want := map[string]string{
		"CodeAWSNoSSO":       "aws_no_sso",
		"CodeAWSRateLimited": "aws_rate_limited",
		"CodeNotFound":       "not_found",
		"CodeNeedsOperator":  "needs_operator",
		"CodeUnavailable":    "unavailable",
		"CodeInvalidParams":  "invalid_params",
	}
	for name, v := range want {
		if consts[name] != v {
			t.Errorf("protocol.go: %s = %q, want %q", name, consts[name], v)
		}
	}
}
