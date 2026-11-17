package server

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/harness"
)

type harnessDebugResult struct {
	Type  string             `json:"type"`
	Debug harness.DebugState `json:"debug"`
}

func TestHarnessDebugOps(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{debug: true})
	run := harnessUp(t, srv, "s1", map[string]any{"session": "s1", "scenario": "plain", "debug": true, "wait": true})
	if !run.Debug || run.State != harness.StateReady || run.DebugLaunch == nil {
		t.Fatalf("run = %+v", run)
	}

	var st harnessDebugResult
	rpcOK(t, srv, "s1", "harness.debug", map[string]any{"run": run.ID}, &st)
	if st.Type != "harness_debug" || st.Debug.Holder == nil || st.Debug.Holder.ClientName != "vscode" || st.Debug.HostPort == 0 {
		t.Fatalf("status = %+v", st)
	}
	// A session reads the state but never takes the debugger from a person.
	for _, op := range []string{harness.DebugTake, harness.DebugRelease} {
		wantHarnessCode(t, "session "+op, rpc(t, srv, "s1", "harness.debug", map[string]any{"run": run.ID, "op": op}), CodeNeedsOperator)
	}
	rpcOK(t, srv, "", "harness.debug", map[string]any{"run": run.ID, "op": harness.DebugTake}, &st)
	if st.Debug.Dropped == nil || st.Debug.Dropped.ClientName != "vscode" {
		t.Fatalf("take = %+v", st)
	}
	if rows := ledgerRows(t, srv, domain.EventHarnessDebug); len(rows) != 1 || rows[0]["op"] != "take" {
		t.Fatalf("harness.debug rows = %v", rows)
	}
	wantHarnessCode(t, "bad op", rpc(t, srv, "", "harness.debug", map[string]any{"run": run.ID, "op": "pause"}), CodeInvalidParams)
	wantHarnessCode(t, "other session", rpc(t, srv, "s2", "harness.debug", map[string]any{"run": run.ID}), CodeSessionScope)
}

// TestDebugCheckNeedsOperator: a debug check waits for a person's debugger
// and can take the run's debugger lease, so a session caller is refused
// before anything else; the operator reaches the manager.
func TestDebugCheckNeedsOperator(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{debug: true})
	run := harnessUp(t, srv, "s1", map[string]any{"session": "s1", "scenario": "plain", "debug": true, "wait": true})
	for _, params := range []map[string]any{
		{"run": run.ID, "name": "fake", "debug": true},
		{"run": run.ID, "name": "fake", "debug": true, "take": true},
		{"name": "fake", "debug": true},
		{"run": "nope", "debug": true},
	} {
		wantHarnessCode(t, "session caller", rpc(t, srv, "s1", "harness.check", params), CodeNeedsOperator)
	}
	wantHarnessCode(t, "no name", rpc(t, srv, "", "harness.check", map[string]any{"run": run.ID, "debug": true}), CodeInvalidParams)
	wantHarnessCode(t, "unknown check", rpc(t, srv, "", "harness.check", map[string]any{"run": run.ID, "name": "nope", "debug": true}), CodeInvalidParams)
	// The operator's call reaches the adapter registry: this host has no
	// python adapter.
	data := wantHarnessCode(t, "python check", rpc(t, srv, "", "harness.check", map[string]any{"run": run.ID, "name": "fake", "debug": true}), harness.CodeDebugUnsupported)
	if data["lang"] != "python" {
		t.Fatalf("data = %v", data)
	}
	// A plain check of the same run still answers a session.
	var out struct {
		Checks []harness.CheckRun `json:"checks"`
	}
	rpcOK(t, srv, "s1", "harness.check", map[string]any{"run": run.ID, "name": "fake"}, &out)
	if len(out.Checks) != 1 || out.Checks[0].DebugLang != "python" || out.Checks[0].Debug {
		t.Fatalf("plain check = %+v", out)
	}
}

func TestHarnessSourceFiles(t *testing.T) {
	srv, dir := harnessServer(t, harnessSetup{})
	if err := os.MkdirAll(filepath.Join(dir, "cmd"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"cmd/main.go": "package main\n", ".env": "SECRET=1\n", "key.pem": "x"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ciGit(t, dir, "add", "-A")
	ciGit(t, dir, "commit", "-q", "-m", "more")
	run := harnessUp(t, srv, "s1", map[string]any{"session": "s1", "scenario": "plain", "wait": true})

	var files struct {
		Type  string   `json:"type"`
		Root  string   `json:"root"`
		Files []string `json:"files"`
	}
	rpcOK(t, srv, "s1", "harness.files", map[string]any{"run": run.ID}, &files)
	if files.Type != "harness_files" || files.Root != "/src" ||
		!slices.Contains(files.Files, "cmd/main.go") || !slices.Contains(files.Files, ".aiman/harness.yaml") {
		t.Fatalf("files = %+v", files)
	}
	if slices.Contains(files.Files, ".env") || slices.Contains(files.Files, "key.pem") {
		t.Fatalf("secret-named files listed: %v", files.Files)
	}

	var page struct {
		Type string `json:"type"`
		Path string `json:"path"`
		Data string `json:"data"`
		EOF  bool   `json:"eof"`
	}
	rpcOK(t, srv, "s1", "harness.file", map[string]any{"run": run.ID, "path": "cmd/main.go"}, &page)
	if page.Type != "harness_file" || page.Path != "cmd/main.go" || page.Data != "package main\n" || !page.EOF {
		t.Fatalf("page = %+v", page)
	}
	// A stack frame names the container path; it maps to the same file.
	rpcOK(t, srv, "s1", "harness.file", map[string]any{"run": run.ID, "path": "/src/cmd/main.go"}, &page)
	if page.Path != "cmd/main.go" {
		t.Fatalf("container path page = %+v", page)
	}
	for _, bad := range []string{"../../../etc/passwd", ".env", "key.pem", "/etc/passwd", "/src/../run.json"} {
		wantHarnessCode(t, bad, rpc(t, srv, "s1", "harness.file", map[string]any{"run": run.ID, "path": bad}), CodeForbidden)
	}
	wantHarnessCode(t, "missing", rpc(t, srv, "s1", "harness.file", map[string]any{"run": run.ID, "path": "nope.go"}), CodeNotFound)
	wantHarnessCode(t, "other session", rpc(t, srv, "s2", "harness.file", map[string]any{"run": run.ID, "path": "go.mod"}), CodeSessionScope)
}
