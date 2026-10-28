package usecase

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// tmuxTextAllowlist names the only non-test files that may carry tmux text:
// the legacy-row hint, badge and stop command shown to the user. aiman itself
// never runs tmux.
var tmuxTextAllowlist = map[string]bool{
	"internal/domain/legacy_tmux.go": true,
	"internal/ui/legacy_tmux.go":     true,
}

// TestNoTmuxOutsideLegacyText fails when a non-test Go file in the module
// holds a string literal that runs or names a tmux command (any literal
// containing "tmux " or equal to "tmux") or names mosh, which only tmux attach
// used. Comments are not checked.
func TestNoTmuxOutsideLegacyText(t *testing.T) {
	root := guardModuleRoot(t)
	var violations []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch {
			case rel == ".git", rel == "docs", rel == "vendor", rel == "graphify-out",
				strings.HasPrefix(d.Name(), "."), d.Name() == "testdata":
				if rel != "." {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") || tmuxTextAllowlist[rel] {
			return nil
		}
		violations = append(violations, tmuxLiterals(t, path, rel)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("tmux or mosh text outside the legacy allowlist (aiman never runs either):\n%s", strings.Join(violations, "\n"))
	}
}

func tmuxLiterals(t *testing.T, path, rel string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		val, err := strconv.Unquote(lit.Value)
		if err != nil {
			val = lit.Value
		}
		if strings.Contains(val, "tmux ") || strings.TrimSpace(val) == "tmux" || strings.Contains(val, "mosh") {
			out = append(out, rel+":"+strconv.Itoa(fset.Position(lit.Pos()).Line)+": "+lit.Value)
		}
		return true
	})
	return out
}

func guardModuleRoot(t *testing.T) string {
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
