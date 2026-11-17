package console

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/harnesstest"
)

// An editor debugging a Python run reaches debugpy's subprocess sessions
// through the editor port. It runs only with AIMAN_TEST_DOCKER=1.
func TestPythonSubprocessThroughEditorPort(t *testing.T) {
	r := harnesstest.UpPythonWrapRun(t)
	run := harness.Run{
		ID: "r1", Session: "s1", Scenario: "dbg", State: harness.StateReady, Debug: true, DebugLaunch: &r.Launch,
		Ports: []harness.RunPort{{Name: harness.RelayDAP, Service: harness.ServiceApp, HostPort: r.HostPort}},
	}
	rpc := &fakeRPC{answer: func(method string, _ map[string]any) (json.RawMessage, error) {
		if method == "harness.debug" {
			return json.RawMessage(`{"type":"harness_debug","debug":{"run":"r1","holder":null,"host_port":1,"refused":0}}`), nil
		}
		return json.Marshal(map[string]any{"type": "harness_run", "run": run})
	}}
	s, host := serveConsole(t, rpc)
	t.Cleanup(s.closeEditor)
	w := do(t, s, http.MethodGet, host, "/api/debug?run=r1", testToken, "", "")
	var v struct {
		EditorPort int            `json:"editor_port"`
		Editor     map[string]any `json:"editor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || v.EditorPort == 0 {
		t.Fatalf("debug view %s: %v", w.Body, err)
	}
	// VS Code fills ${workspaceFolder}; debugpy maps it to /src itself.
	const ws = "/home/me/checkout"
	b, err := json.Marshal(v.Editor)
	if err != nil {
		t.Fatal(err)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(strings.ReplaceAll(string(b), "${workspaceFolder}", ws)), &args); err != nil {
		t.Fatal(err)
	}
	root := harnesstest.NewSession(t, harnesstest.DialDAP(t, v.EditorPort))
	root.Must("initialize", harnesstest.PythonInit)
	root.Start("attach", args)
	root.Event("initialized")
	harnesstest.StopAt(t, root, ws+"/child.py", harnesstest.PythonChildLine)

	// The subprocess's configuration points at the editor port, so VS Code
	// connects it there, under the editor's lease.
	sd := root.ReverseRequest("startDebugging")
	sdArgs, _ := sd["arguments"].(map[string]any)
	cfg, _ := sdArgs["configuration"].(map[string]any)
	if p, _ := cfg["debugServer"].(float64); int(p) != v.EditorPort {
		t.Fatalf("child configuration %v lacks debugServer %d", cfg, v.EditorPort)
	}
	child := harnesstest.NewSession(t, harnesstest.DialDAP(t, v.EditorPort))
	child.Must("initialize", harnesstest.PythonInit)
	child.Start("attach", cfg)
	child.Event("initialized")
	top := harnesstest.StopAt(t, child, ws+"/grandchild.py", harnesstest.PythonGrandchildLine)
	src, _ := top["source"].(map[string]any)
	if src["path"] != ws+"/grandchild.py" {
		t.Fatalf("frame source %v, want the editor's %s/grandchild.py", src["path"], ws)
	}
}
