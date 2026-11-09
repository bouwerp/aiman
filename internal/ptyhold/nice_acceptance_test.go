//go:build acceptance

package ptyhold

// Acceptance tests for P3 of docs/specs/serve-load-guard.md (Behaviour 4,
// "Agents yield to serve").
//
// Contract pinned here:
//
//	func childShell(command string, nice int) string
//	Spec.AgentNice int `json:"agent_nice,omitempty"`   (request.json)
//	ptyruntime.Spec.AgentNice int, copied into ptyhold.Spec by Manager.Create
//
// nice is an increment (`nice -n <n>`), clamped to [0,19]; 0 (and anything
// clamped to 0) emits no wrapper. Expected niceness is computed relative to the
// test process's own, so the suite also passes under `nice -n 10 go test`.

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// procNice reads the niceness of pid from /proc/<pid>/stat (field 19).
func procNice(t *testing.T, pid int) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		t.Fatalf("read stat of %d: %v", pid, err)
	}
	s := string(raw)
	// comm (field 2) may hold spaces; fields after it start past the last ')'.
	rest := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
	// rest[0] is field 3 (state), so field 19 is rest[16].
	n, err := strconv.Atoi(rest[16])
	if err != nil {
		t.Fatalf("parse niceness from %q: %v", s, err)
	}
	return n
}

func requireLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("niceness is read from /proc and coreutils nice; Linux only")
	}
	if _, err := exec.LookPath("nice"); err != nil {
		t.Skip("nice not on PATH")
	}
}

// baseNice is the test process's niceness; it must leave room for an increment
// to be observable.
func baseNice(t *testing.T) int {
	t.Helper()
	b := procNice(t, os.Getpid())
	if b >= 19 {
		t.Skipf("test process already at niceness %d; an increment is not observable", b)
	}
	return b
}

func want(base, inc int) int {
	if inc < 0 {
		inc = 0
	}
	if inc > 19 {
		inc = 19
	}
	if base+inc > 19 {
		return 19
	}
	return base + inc
}

// runShellLine runs line the way the holder does (minus the login profile) and
// returns its stdout lines.
func runShellLine(t *testing.T, line string) []string {
	t.Helper()
	cmd := exec.Command("bash", "-c", line)
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash -c %q: %v\n%s", line, err, out)
	}
	return strings.Fields(strings.TrimSpace(string(out)))
}

func TestAcceptanceChildShellWrapsInNice(t *testing.T) {
	got := childShell("claude --resume abc", 10)
	if !strings.Contains(got, "nice -n 10") {
		t.Fatalf("childShell(_, 10) must wrap the command in `nice -n 10`: %s", got)
	}
	if !strings.Contains(got, "claude --resume abc") {
		t.Fatalf("command missing from the shell line: %s", got)
	}
	// The existing guarantees still hold with a wrapper.
	if !strings.HasPrefix(got, `export PATH="$HOME/.npm-global/bin:$PATH:`) {
		t.Fatalf("user npm prefix must still come first: %s", got)
	}
	termAt := strings.Index(got, "TERM="+defaultTERM)
	niceAt := strings.Index(got, "nice -n 10")
	if termAt < 0 || termAt > niceAt {
		t.Fatalf("TERM must still be exported before the agent starts: %s", got)
	}
}

func TestAcceptanceChildShellZeroHasNoWrapper(t *testing.T) {
	for _, n := range []int{0, -1, -20} {
		got := childShell("claude", n)
		if strings.Contains(got, "nice") {
			t.Fatalf("childShell(_, %d) must not mention nice at all: %s", n, got)
		}
		if got != childShell("claude", 0) {
			t.Fatalf("childShell(_, %d) must equal the nice-0 line", n)
		}
	}
}

func TestAcceptanceChildShellClampsAbove19(t *testing.T) {
	for _, n := range []int{20, 40, 1000} {
		got := childShell("claude", n)
		if !strings.Contains(got, "nice -n 19") {
			t.Fatalf("childShell(_, %d) must clamp to `nice -n 19`: %s", n, got)
		}
		if strings.Contains(got, strconv.Itoa(n)) {
			t.Fatalf("childShell(_, %d) must not pass the unclamped value: %s", n, got)
		}
	}
}

func TestAcceptanceChildShellRunsCommandAtNiceness(t *testing.T) {
	requireLinux(t)
	base := baseNice(t)
	cases := []struct {
		inc  int
		want int
	}{
		{10, want(base, 10)},
		{0, base},
		{-5, base},
		{99, want(base, 19)},
	}
	for _, tc := range cases {
		// `nice` with no arguments prints its own niceness.
		lines := runShellLine(t, childShell("nice", tc.inc))
		if len(lines) != 1 || lines[0] != strconv.Itoa(tc.want) {
			t.Errorf("childShell(nice, %d) printed %v, want [%d] (parent at %d)", tc.inc, lines, tc.want, base)
		}
	}
}

// Real agent commands are shell text: env assignments, `cd … &&`, `;`,
// launcher scripts. Every part of the command must start niced, and the shell
// syntax must survive the wrapper.
func TestAcceptanceChildShellNicesCompoundCommands(t *testing.T) {
	requireLinux(t)
	base := baseNice(t)
	cmd := `X=ok sh -c 'nice; echo "$X"' && cd / && nice; echo "q'uote"`
	lines := runShellLine(t, childShell(cmd, 10))
	w := strconv.Itoa(want(base, 10))
	expect := []string{w, "ok", w, "q'uote"}
	if strings.Join(lines, "|") != strings.Join(expect, "|") {
		t.Fatalf("compound command under childShell(_, 10) printed %v, want %v", lines, expect)
	}
}

func TestAcceptanceRequestCarriesAgentNice(t *testing.T) {
	raw, err := json.Marshal(Spec{ID: "a", Dir: "/", Command: "claude", AgentNice: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"agent_nice":10`) {
		t.Fatalf("request.json must carry agent_nice: %s", raw)
	}
	raw, err = json.Marshal(Spec{ID: "a", Dir: "/", Command: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "agent_nice") {
		t.Fatalf("a zero agent_nice must be omitted (older holders read the same file): %s", raw)
	}
	var back Spec
	if err := json.Unmarshal([]byte(`{"id":"a","dir":"/","command":"c","agent_nice":7}`), &back); err != nil {
		t.Fatal(err)
	}
	if back.AgentNice != 7 {
		t.Fatalf("agent_nice did not round-trip: %+v", back)
	}
}

// runHolder runs the holder in-process over a request and waits for it to
// exit. The test process is the holder, so its niceness is the holder's.
func runHolder(t *testing.T, spec Spec) string {
	t.Helper()
	// Short root: the holder binds a unix socket under it.
	root, err := os.MkdirTemp("", "pn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	spec.ID = "n"
	spec.Dir = root
	dir := Dir(root, spec.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, RequestFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- Run(root, spec.ID) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("holder Run: %v", err)
		}
	case <-time.After(30 * time.Second):
		_ = os.WriteFile(filepath.Join(dir, KillFile), nil, 0o600)
		<-done
		t.Fatal("holder did not exit within 30s")
	}
	return root
}

func readNiceFile(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("agent did not record its niceness: %v", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return n
}

func TestAcceptanceHolderStaysAtBaseWhileAgentIsNiced(t *testing.T) {
	requireLinux(t)
	base := baseNice(t)
	out := filepath.Join(t.TempDir(), "agent-nice")
	// The agent records its own niceness; the test process is the holder.
	runHolder(t, Spec{Command: "nice > " + out, AgentNice: 10})

	if got := readNiceFile(t, out); got != want(base, 10) {
		t.Fatalf("agent ran at niceness %d, want %d (holder at %d)", got, want(base, 10), base)
	}
	if got := procNice(t, os.Getpid()); got != base {
		t.Fatalf("holder niceness changed to %d, must stay at %d", got, base)
	}
}

func TestAcceptanceHolderRequestWithoutAgentNiceRunsAtBase(t *testing.T) {
	requireLinux(t)
	base := baseNice(t)
	out := filepath.Join(t.TempDir(), "agent-nice")
	// A request written by a serve that predates agent_nice.
	runHolder(t, Spec{Command: "nice > " + out})
	if got := readNiceFile(t, out); got != base {
		t.Fatalf("agent ran at niceness %d without agent_nice, want %d", got, base)
	}
}

func TestAcceptanceHolderClampsAbsurdAgentNice(t *testing.T) {
	requireLinux(t)
	base := baseNice(t)
	out := filepath.Join(t.TempDir(), "agent-nice")
	runHolder(t, Spec{Command: "nice > " + out, AgentNice: 500})
	if got := readNiceFile(t, out); got != want(base, 19) {
		t.Fatalf("agent ran at niceness %d with agent_nice 500, want %d", got, want(base, 19))
	}
	if got := procNice(t, os.Getpid()); got != base {
		t.Fatalf("holder niceness changed to %d, must stay at %d", got, base)
	}
}

// ptyruntime imports ptyhold, so this package's tests cannot import it. The
// plumbing is pinned from source instead: ptyruntime.Spec has an int AgentNice
// and Manager.Create copies it into the ptyhold.Spec it hands to Spawn.
func TestAcceptanceRuntimePassesAgentNice(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, filepath.Join("..", "ptyruntime"), func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ok := pkgs["ptyruntime"]
	if !ok {
		t.Fatal("package ptyruntime not found")
	}
	var specField, copied bool
	for _, f := range pkg.Files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.TypeSpec:
				st, ok := x.Type.(*ast.StructType)
				if x.Name.Name != "Spec" || !ok {
					return true
				}
				for _, fld := range st.Fields.List {
					id, isIdent := fld.Type.(*ast.Ident)
					for _, name := range fld.Names {
						if name.Name == "AgentNice" && isIdent && id.Name == "int" {
							specField = true
						}
					}
				}
			case *ast.CompositeLit:
				sel, ok := x.Type.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Spec" {
					return true
				}
				if pkgID, ok := sel.X.(*ast.Ident); !ok || pkgID.Name != "ptyhold" {
					return true
				}
				for _, el := range x.Elts {
					kv, ok := el.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "AgentNice" {
						if v, ok := kv.Value.(*ast.SelectorExpr); ok && v.Sel.Name == "AgentNice" {
							copied = true
						}
					}
				}
			}
			return true
		})
	}
	if !specField {
		t.Error("ptyruntime.Spec must have an `AgentNice int` field")
	}
	if !copied {
		t.Error("ptyruntime must copy spec.AgentNice into the ptyhold.Spec it spawns (AgentNice: spec.AgentNice)")
	}
}
