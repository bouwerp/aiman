//go:build pileup_a

package main

// Contract (docs/specs/client-pileup-guard.md, Behaviour P1, allowlist):
//
//   - No new symbols. rpcMethods in cmd/aiman/rpc_cmd.go must list every
//     method the laptop passes to usecase.ServeRPC, directly or through a
//     wrapper, in non-test code anywhere in the module (resources.get and the
//     agents.* methods are missing today).
//
// How call sites are found: every call to ServeRPC, and to any function,
// method, func-valued variable or struct field that forwards one of its own
// parameters as ServeRPC's method argument (found to a fixed point, including
// names bound to the value usecase.ServeRPC such as `rpc: usecase.ServeRPC`).
// The method argument of each such call must be a string literal, a string
// constant, or a selector `x.F` whose values are the string literals keyed F
// in composite literals of the same package (todoOp{Method: "todo.add"}).
// Anything else fails the test, so a new indirection cannot hide a method.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const alModule = "github.com/bouwerp/aiman"

type alFile struct {
	dir  string
	file *ast.File
	// imports maps a local package name to the module-relative dir.
	imports map[string]string
}

type alKey struct {
	dir  string
	name string
}

func alRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

func alParse(t *testing.T, root string) []alFile {
	t.Helper()
	fset := token.NewFileSet()
	var files []alFile
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata" || name == "third_party" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return perr
		}
		rel, _ := filepath.Rel(root, filepath.Dir(path))
		af := alFile{dir: filepath.ToSlash(rel), file: f, imports: map[string]string{}}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if !strings.HasPrefix(p, alModule+"/") {
				continue
			}
			local := p[strings.LastIndex(p, "/")+1:]
			if imp.Name != nil {
				local = imp.Name.Name
			}
			af.imports[local] = strings.TrimPrefix(p, alModule+"/")
		}
		files = append(files, af)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// alCallee names what a call invokes: (dir, name). A pkg.Name selector
// resolves to that package's dir; any other x.Name or a bare Name is looked
// up in the caller's own package.
func alCallee(f alFile, call *ast.CallExpr) (alKey, bool) {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return alKey{f.dir, fn.Name}, true
	case *ast.SelectorExpr:
		if x, ok := fn.X.(*ast.Ident); ok {
			if dir, ok := f.imports[x.Name]; ok {
				return alKey{dir, fn.Sel.Name}, true
			}
		}
		return alKey{f.dir, fn.Sel.Name}, true
	}
	return alKey{}, false
}

// alIsServeRPCValue reports an expression that is the function value ServeRPC.
func alIsServeRPCValue(f alFile, e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name == "ServeRPC" && f.dir == "internal/usecase"
	case *ast.SelectorExpr:
		x, ok := v.X.(*ast.Ident)
		return ok && v.Sel.Name == "ServeRPC" && f.imports[x.Name] == "internal/usecase"
	}
	return false
}

// alFunc is one function body with its parameter names in order.
type alFunc struct {
	key    alKey
	params []string
	body   *ast.BlockStmt
	file   alFile
}

func alParams(ft *ast.FuncType) []string {
	var out []string
	for _, fld := range ft.Params.List {
		if len(fld.Names) == 0 {
			out = append(out, "_")
			continue
		}
		for _, n := range fld.Names {
			out = append(out, n.Name)
		}
	}
	return out
}

func alFuncs(files []alFile) []alFunc {
	var out []alFunc
	for _, f := range files {
		ast.Inspect(f.file, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.FuncDecl:
				if v.Body != nil {
					out = append(out, alFunc{alKey{f.dir, v.Name.Name}, alParams(v.Type), v.Body, f})
				}
			case *ast.ValueSpec:
				for i, val := range v.Values {
					if lit, ok := val.(*ast.FuncLit); ok && i < len(v.Names) {
						out = append(out, alFunc{alKey{f.dir, v.Names[i].Name}, alParams(lit.Type), lit.Body, f})
					}
				}
			case *ast.AssignStmt:
				for i, val := range v.Rhs {
					lit, ok := val.(*ast.FuncLit)
					if !ok || i >= len(v.Lhs) {
						continue
					}
					if name := alLHSName(v.Lhs[i]); name != "" {
						out = append(out, alFunc{alKey{f.dir, name}, alParams(lit.Type), lit.Body, f})
					}
				}
			case *ast.KeyValueExpr:
				if lit, ok := v.Value.(*ast.FuncLit); ok {
					if k, ok := v.Key.(*ast.Ident); ok {
						out = append(out, alFunc{alKey{f.dir, k.Name}, alParams(lit.Type), lit.Body, f})
					}
				}
			}
			return true
		})
	}
	return out
}

func alLHSName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return v.Sel.Name
	}
	return ""
}

// alWrappers finds every name whose calls carry a ServeRPC method, and the
// argument index it is at.
func alWrappers(files []alFile, funcs []alFunc) map[alKey]int {
	w := map[alKey]int{{"internal/usecase", "ServeRPC"}: 2}
	// Names bound to the value usecase.ServeRPC.
	for _, f := range files {
		ast.Inspect(f.file, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.KeyValueExpr:
				if k, ok := v.Key.(*ast.Ident); ok && alIsServeRPCValue(f, v.Value) {
					w[alKey{f.dir, k.Name}] = 2
				}
			case *ast.ValueSpec:
				for i, val := range v.Values {
					if i < len(v.Names) && alIsServeRPCValue(f, val) {
						w[alKey{f.dir, v.Names[i].Name}] = 2
					}
				}
			case *ast.AssignStmt:
				for i, val := range v.Rhs {
					if i < len(v.Lhs) && alIsServeRPCValue(f, val) {
						if name := alLHSName(v.Lhs[i]); name != "" {
							w[alKey{f.dir, name}] = 2
						}
					}
				}
			}
			return true
		})
	}
	for changed := true; changed; {
		changed = false
		for _, fn := range funcs {
			ast.Inspect(fn.body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				key, ok := alCallee(fn.file, call)
				if !ok {
					return true
				}
				idx, ok := w[key]
				if !ok || idx >= len(call.Args) {
					return true
				}
				id, ok := call.Args[idx].(*ast.Ident)
				if !ok {
					return true
				}
				for pi, p := range fn.params {
					if p == id.Name {
						if _, seen := w[fn.key]; !seen {
							w[fn.key] = pi
							changed = true
						}
					}
				}
				return true
			})
		}
	}
	return w
}

// alStringConsts are package-level string constants per dir.
func alStringConsts(files []alFile) map[alKey]string {
	out := map[alKey]string{}
	for _, f := range files {
		for _, decl := range f.file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, val := range vs.Values {
					if lit, ok := val.(*ast.BasicLit); ok && lit.Kind == token.STRING && i < len(vs.Names) {
						s, _ := strconv.Unquote(lit.Value)
						out[alKey{f.dir, vs.Names[i].Name}] = s
					}
				}
			}
		}
	}
	return out
}

// alKeyedLiterals are the string literals keyed field in composite literals of
// one package.
func alKeyedLiterals(files []alFile, dir, field string) []string {
	var out []string
	for _, f := range files {
		if f.dir != dir {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			if k, ok := kv.Key.(*ast.Ident); ok && k.Name == field {
				if lit, ok := kv.Value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					s, _ := strconv.Unquote(lit.Value)
					out = append(out, s)
				}
			}
			return true
		})
	}
	return out
}

// TestAcceptanceRPCAllowlistCoversServeRPCCallSites: every method the laptop
// passes to usecase.ServeRPC in non-test code is in rpcMethods, so no TUI call
// is refused by `aiman rpc` before it dials serve.
func TestAcceptanceRPCAllowlistCoversServeRPCCallSites(t *testing.T) {
	root := alRepoRoot(t)
	files := alParse(t, root)
	funcs := alFuncs(files)
	wrappers := alWrappers(files, funcs)
	consts := alStringConsts(files)

	type site struct{ method, pos string }
	var sites []site
	var unresolved []string
	for _, fn := range funcs {
		params := map[string]bool{}
		for _, p := range fn.params {
			params[p] = true
		}
		ast.Inspect(fn.body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			key, ok := alCallee(fn.file, call)
			if !ok {
				return true
			}
			idx, ok := wrappers[key]
			if !ok || idx >= len(call.Args) {
				return true
			}
			where := fn.file.dir + ":" + fn.key.name
			switch arg := call.Args[idx].(type) {
			case *ast.BasicLit:
				if arg.Kind == token.STRING {
					s, _ := strconv.Unquote(arg.Value)
					sites = append(sites, site{s, where})
					return true
				}
			case *ast.Ident:
				if params[arg.Name] {
					return true // a wrapper; its callers are checked
				}
				if s, ok := consts[alKey{fn.file.dir, arg.Name}]; ok {
					sites = append(sites, site{s, where})
					return true
				}
			case *ast.SelectorExpr:
				if x, ok := arg.X.(*ast.Ident); ok {
					if dir, ok := fn.file.imports[x.Name]; ok {
						if s, ok := consts[alKey{dir, arg.Sel.Name}]; ok {
							sites = append(sites, site{s, where})
							return true
						}
					}
				}
				vals := alKeyedLiterals(files, fn.file.dir, arg.Sel.Name)
				if len(vals) > 0 {
					for _, s := range vals {
						sites = append(sites, site{s, where + " via ." + arg.Sel.Name})
					}
					return true
				}
			}
			unresolved = append(unresolved, where)
			return true
		})
	}

	if len(sites) == 0 {
		t.Fatal("found no ServeRPC call sites; the parser is broken")
	}
	// Known call sites must be found, or the walk is missing a form.
	found := map[string]bool{}
	for _, s := range sites {
		found[s.method] = true
	}
	for _, want := range []string{"resources.get", "todo.summary", "todo.add", "ci.get", "agents.status", "usage.get", "session.get"} {
		if !found[want] {
			t.Errorf("the call-site walk did not find %q", want)
		}
	}
	if len(unresolved) > 0 {
		sort.Strings(unresolved)
		t.Errorf("ServeRPC method arguments the test cannot resolve to a literal (use a literal or a string constant): %v", unresolved)
	}

	missing := map[string][]string{}
	for _, s := range sites {
		if !rpcMethods[s.method] {
			missing[s.method] = append(missing[s.method], s.pos)
		}
	}
	if len(missing) > 0 {
		var names []string
		for m := range missing {
			names = append(names, m)
		}
		sort.Strings(names)
		for _, m := range names {
			t.Errorf("method %q is passed to ServeRPC (%s) but is not in rpcMethods", m, strings.Join(missing[m], ", "))
		}
	}
}
