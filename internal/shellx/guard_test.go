//go:build acceptance

package shellx

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// shellDashC matches a literal that hands the rest of itself to a shell.
var shellDashC = regexp.MustCompile(`\b(bash|sh)\s+-l?c\b`)

// shellCommandStart matches a literal that is itself a remote command line,
// the kind Execute and WriteFile hand to the login shell on the host.
var shellCommandStart = regexp.MustCompile(`^(cat >|test -|find |git -C|exec |mkdir -p|rm -rf|ls |cd )`)

// TestOneShellQuoter fails when a non-test Go file outside internal/shellx
// declares its own shellQuote, or quotes for a shell with Go's %q: a literal
// that holds both "bash -c"/"sh -c" and %q, or a call that passes "-c" next to
// a format string holding %q. Go's %q is a Go string literal, not a shell word:
// $(...) and backticks inside it still run. Use shellx.Quote.
func TestOneShellQuoter(t *testing.T) {
	root := moduleRoot(t)
	var violations []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && (skipDir(rel, d.Name())) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		violations = append(violations, shellQuoteOffences(t, path, rel)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("%d shell-quoting offences (use shellx.Quote, not %%q or a private shellQuote):\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}

func skipDir(rel, name string) bool {
	switch {
	case rel == "internal/shellx", rel == "docs", rel == "vendor", rel == "graphify-out",
		rel == "third_party", strings.HasPrefix(name, "."), name == "testdata":
		return true
	}
	return false
}

func shellQuoteOffences(t *testing.T, path, rel string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	var out []string
	at := func(p token.Pos, what string) {
		out = append(out, rel+":"+strconv.Itoa(fset.Position(p).Line)+": "+what)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			if x.Name.Name == "shellQuote" {
				at(x.Pos(), "declares shellQuote")
			}
		case *ast.BasicLit:
			v := literal(x)
			if strings.Contains(v, "%q") && shellDashC.MatchString(v) {
				at(x.Pos(), "%q inside a shell -c string: "+x.Value)
			}
			if strings.Contains(v, "%q") && shellCommandStart.MatchString(v) {
				at(x.Pos(), "%q in a remote command line: "+x.Value)
			}
		case *ast.CallExpr:
			if lit := dashCFormatArg(x); lit != nil {
				at(lit.Pos(), "%q in a format passed with -c: "+lit.Value)
			}
		}
		return true
	})
	return out
}

// dashCFormatArg returns a %q format literal among call's arguments (directly
// or as the first argument of a nested call such as fmt.Sprintf) when another
// argument is the literal "-c" or "-lc".
func dashCFormatArg(call *ast.CallExpr) *ast.BasicLit {
	hasDashC := false
	var format *ast.BasicLit
	for _, arg := range call.Args {
		if lit, ok := arg.(*ast.BasicLit); ok {
			if v := literal(lit); v == "-c" || v == "-lc" {
				hasDashC = true
			}
		}
		inner, ok := arg.(*ast.CallExpr)
		if !ok || len(inner.Args) == 0 {
			continue
		}
		if lit, ok := inner.Args[0].(*ast.BasicLit); ok && strings.Contains(literal(lit), "%q") {
			format = lit
		}
	}
	if hasDashC {
		return format
	}
	return nil
}

func literal(lit *ast.BasicLit) string {
	if lit.Kind != token.STRING {
		return ""
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return lit.Value
	}
	return v
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found from %s: %v", file, err)
	}
	return root
}
