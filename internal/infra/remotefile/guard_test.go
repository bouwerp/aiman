package remotefile

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// writeFileAllowlist names the functions that may call a remote WriteFile
// directly. Each writes a transient /tmp file or a config file outside an
// agent's cached prompt prefix, or is the compare-first helper itself.
var writeFileAllowlist = map[string]bool{
	"SendPrompt":                      true,
	"SendPromptTagged":                true,
	"DeliverInitialPromptPTY":         true,
	"CaptureRestartSessionSummaryPTY": true,
	"CreateSessionOnRemote":           true,
	"CreatePTYSession":                true,
	"SendSessionPrompt":               true,
	"ApplyKiloAllowEnv":               true,
	"EnsureClaudeWorkspaceTrusted":    true,
	// Writes only when a path is newly trusted.
	"EnsurePiWorkspaceTrusted": true,
	"WriteIfChanged":           true,
	// Compares sha256 for all files in one listing call before writing.
	"PushUserChecklists": true,
}

// scaffoldingDirs are the packages whose writers can touch files that sit in
// an agent's cached prompt prefix, relative to the module root.
var scaffoldingDirs = []string{
	"internal/infra/skills",
	"internal/contextstore",
	"internal/usecase",
	"internal/server",
	"internal/todo",
	"internal/quality",
}

// TestScaffoldingWritersCompareFirst fails when a scaffolding writer calls
// WriteFile directly instead of going through remotefile.WriteIfChanged, which
// leaves unchanged files (and the agent's prompt cache) untouched.
func TestScaffoldingWritersCompareFirst(t *testing.T) {
	root := moduleRoot(t)
	var violations []string
	for _, dir := range scaffoldingDirs {
		abs := filepath.Join(root, dir)
		if _, err := os.Stat(abs); os.IsNotExist(err) {
			continue
		}
		entries, err := os.ReadDir(abs)
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			violations = append(violations, directWriteFileCalls(t, filepath.Join(abs, name), filepath.Join(dir, name))...)
		}
	}
	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("%s calls WriteFile directly; use remotefile.WriteIfChanged so unchanged files keep their bytes and mtime", v)
	}
}

// directWriteFileCalls returns "file:line func" for each X.WriteFile call in
// path whose receiver is not the os package and whose enclosing top-level
// function is not allowlisted.
func directWriteFileCalls(t *testing.T, path, rel string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", rel, err)
	}
	var out []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || writeFileAllowlist[fn.Name.Name] {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "WriteFile" {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "os" {
				return true
			}
			pos := fset.Position(call.Pos())
			out = append(out, rel+":"+strconv.Itoa(pos.Line)+" "+fn.Name.Name)
			return true
		})
	}
	return out
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..")
}
