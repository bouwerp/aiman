package gateway

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"testing"
)

// serveMethods reads the method names out of serve's dispatch switch, so a
// renamed or removed method fails this package's tests.
func serveMethods(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "../server/server.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	methods := map[string]bool{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "dispatch" || fn.Recv == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			cc, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, e := range cc.List {
				lit, ok := e.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				m, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				methods[m] = true
			}
			return true
		})
	}
	if len(methods) < 20 || !methods["ping"] || !methods["session.list"] {
		t.Fatalf("could not read serve's dispatch table: %v", methods)
	}
	return methods
}

func TestAllowlistMethodsExist(t *testing.T) {
	serve := serveMethods(t)
	own := toSet(gatewayMethods)
	for _, list := range [][]string{deviceReadMethods, deviceWriteMethods, deviceDestructiveMethods} {
		for _, m := range list {
			if !serve[m] && !own[m] {
				t.Errorf("allowlisted method %q is not handled by serve or the gateway", m)
			}
		}
	}
}

func TestAllowlistDestructiveSet(t *testing.T) {
	want := []string{
		"agents.move_prefix", "agents.update",
		"aws.login_start",
		"gateway.restart_serve",
		"host.logs", "host.restart", "host.update",
		"pty.create", "pty.forget", "pty.input", "pty.kill", "pty.run",
		"review.post",
		"session.create", "session.forget", "session.restart",
	}
	got := slices.Clone(deviceDestructiveMethods)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("destructive %v want %v", got, want)
	}
	for _, m := range deviceDestructiveMethods {
		if deviceAllowed[m] {
			t.Errorf("%s is destructive but also allowed without allow_destructive", m)
		}
	}
}

func TestAllowlistKeepsReviewFixAndApproveOff(t *testing.T) {
	for _, m := range []string{"review.fix", "review.push", "review.assess", "review.approve"} {
		for _, destructive := range []bool{false, true} {
			if deviceMayCall(m, destructive) {
				t.Errorf("%s must stay off the device allowlist (destructive=%v)", m, destructive)
			}
		}
	}
}

func TestDeviceMayCall(t *testing.T) {
	tests := []struct {
		method      string
		destructive bool
		want        bool
	}{
		{"ping", false, true},
		{"session.list", false, true},
		{"pty.capture", false, true},
		{"session.create", false, false},
		{"session.create", true, true},
		{"session.prompt", false, true},
		{"steer.send", false, true},
		{"session.rename", false, true},
		{"session.move", false, true},
		{"session.restart", false, false},
		{"session.restart", true, true},
		{"session.terminate", false, true},
		{"repo.list", false, true},
		{"host.stats", false, true},
		{"todo.approve_design", false, true},
		{"pty.input", false, false},
		{"pty.input", true, true},
		{"push.register", false, true},
		{"pty.kill", false, false},
		{"pty.kill", true, true},
		{"session.forget", false, false},
		{"review.post", true, true},
		{"agents.update", false, false},
		{"aws.status", false, true},
		{"aws.login_status", false, true},
		{"aws.login_start", false, false},
		{"aws.login_start", true, true},
		{"todo.mirror", false, false},
		{"todo.mirror", true, false},
		{"context.put", true, false},
		{"session.register", true, false},
		{"pty.create", false, false},
		{"pty.create", true, true},
		{"pty.run", false, false},
		{"pty.run", true, true},
		{"design.publish", true, false},
		{"handover.write", true, false},
		{"pty.attach", true, false},
		{"", true, false},
		{"nope", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.method+"/"+strconv.FormatBool(tt.destructive), func(t *testing.T) {
			if got := deviceMayCall(tt.method, tt.destructive); got != tt.want {
				t.Fatalf("deviceMayCall(%q, %v) = %v", tt.method, tt.destructive, got)
			}
		})
	}
}
