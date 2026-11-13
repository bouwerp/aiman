package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// openAPISpec is the phone contract the gateway serves.
const openAPISpec = "../../api/openapi.yaml"

// gatewayOwnedMethods are RPC methods the gateway answers itself; serve never
// sees them.
var gatewayOwnedMethods = []string{"push.register", "push.unregister", "gateway.restart_serve"}

// eventSources are the directories whose code publishes SessionEvents: serve's
// handlers, and the serve command wiring the notify recorder to the hub.
var eventSources = []string{".", "../../cmd/aiman"}

// TestOpenAPIListsEveryMethodAndEvent fails when serve handles a method or
// publishes an event type that api/openapi.yaml does not list, or when the
// spec lists a method nobody handles. Methods and event types are read from
// the source, so a new case in dispatch or a new SessionEvent literal needs a
// spec entry before this passes.
func TestOpenAPIListsEveryMethodAndEvent(t *testing.T) {
	spec := loadOpenAPI(t)
	rpcMethods := specEnum(t, spec, "RpcRequest", "properties", "method")
	streamMethods := specEnum(t, spec, "StreamMethod")
	eventTypes := specEnum(t, spec, "SessionEvent", "properties", "type")

	handled, streams := servedMethods(t)
	for _, m := range handled {
		if !slices.Contains(rpcMethods, m) {
			t.Errorf("method %q is handled by serve but missing from RpcRequest.method in %s", m, openAPISpec)
		}
	}
	for _, m := range streams {
		if !slices.Contains(streamMethods, m) {
			t.Errorf("streaming method %q is handled by serve but missing from StreamMethod in %s", m, openAPISpec)
		}
	}
	for _, m := range rpcMethods {
		if !slices.Contains(handled, m) && !slices.Contains(gatewayOwnedMethods, m) {
			t.Errorf("spec lists method %q that neither serve nor the gateway handles", m)
		}
	}
	for _, ev := range publishedEventTypes(t) {
		if !slices.Contains(eventTypes, ev) {
			t.Errorf("event type %q is published but missing from SessionEvent.type in %s", ev, openAPISpec)
		}
	}
}

func loadOpenAPI(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(openAPISpec)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	var spec map[string]any
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	return spec
}

// specEnum returns the enum at components.schemas.<schema>[.path...].
func specEnum(t *testing.T, spec map[string]any, schema string, path ...string) []string {
	t.Helper()
	keys := append([]string{"components", "schemas", schema}, path...)
	node := any(spec)
	for _, k := range keys {
		m, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("spec: %s is not a mapping", strings.Join(keys, "."))
		}
		node = m[k]
	}
	m, _ := node.(map[string]any)
	list, ok := m["enum"].([]any)
	if !ok {
		t.Fatalf("spec: %s has no enum", strings.Join(keys, "."))
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		s, _ := v.(string)
		out = append(out, s)
	}
	return out
}

func parseDir(t *testing.T, dir string) []*ast.File {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		files = append(files, f)
	}
	return files
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

// servedMethods reads the case labels of Server.dispatch and the methods the
// connection loop hands to a stream (`probe.Method == "..."`).
func servedMethods(t *testing.T) (handled, streams []string) {
	t.Helper()
	for _, f := range parseDir(t, ".") {
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.FuncDecl:
				if n.Name.Name == "dispatch" && n.Recv != nil {
					handled = append(handled, dispatchCases(n)...)
				}
			case *ast.BinaryExpr:
				if n.Op == token.EQL && isProbeMethod(n.X) {
					if s, ok := stringLit(n.Y); ok {
						streams = append(streams, s)
					}
				}
			}
			return true
		})
	}
	if len(handled) == 0 || len(streams) == 0 {
		t.Fatalf("found %d handled and %d streaming methods; did dispatch move?", len(handled), len(streams))
	}
	return handled, streams
}

// isProbeMethod matches probe.Method, the field the stream detectors
// (isPTYAttach, isSessionEvents) compare before dispatch runs.
func isProbeMethod(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Method" {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == "probe"
}

func dispatchCases(fn *ast.FuncDecl) []string {
	var out []string
	for _, stmt := range fn.Body.List {
		sw, ok := stmt.(*ast.SwitchStmt)
		if !ok {
			continue
		}
		for _, c := range sw.Body.List {
			for _, e := range c.(*ast.CaseClause).List {
				if s, ok := stringLit(e); ok {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// publishedEventTypes reads the Type of every SessionEvent composite literal.
func publishedEventTypes(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, dir := range eventSources {
		for _, f := range parseDir(t, dir) {
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || !isSessionEventType(lit.Type) {
					return true
				}
				for _, el := range lit.Elts {
					kv, ok := el.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Type" {
						if s, ok := stringLit(kv.Value); ok && !slices.Contains(out, s) {
							out = append(out, s)
						}
					}
				}
				return true
			})
		}
	}
	if len(out) == 0 {
		t.Fatal("found no published event types")
	}
	return out
}

func isSessionEventType(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name == "SessionEvent"
	case *ast.SelectorExpr:
		x, ok := e.X.(*ast.Ident)
		return ok && x.Name == "server" && e.Sel.Name == "SessionEvent"
	}
	return false
}
