package console

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/harnesstest"
)

// wsTransport carries DAP over the console's bridge, as the page does.
type wsTransport struct {
	ctx context.Context
	ws  *websocket.Conn
}

func (w wsTransport) Send(msg map[string]any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return w.ws.Write(w.ctx, websocket.MessageText, b)
}

func (w wsTransport) Recv() (map[string]any, error) {
	ctx, cancel := context.WithTimeout(w.ctx, 60*time.Second)
	defer cancel()
	_, b, err := w.ws.Read(ctx)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(b, &m)
}

// TestDAPBreakpointHit drives a real debug run through the console: the
// browser's WebSocket, the bridge, the forward (here the host port itself),
// the edge's lease relay and dlv dap in the app container.
func TestDAPBreakpointHit(t *testing.T) {
	r := harnesstest.UpDebugRun(t)
	run := harness.Run{
		ID: "r1", Session: "s1", Scenario: "dbg", State: harness.StateReady, Debug: true, DebugLaunch: &r.Launch,
		Ports: []harness.RunPort{{Name: harness.RelayDAP, Service: harness.ServiceApp, HostPort: r.HostPort}},
	}
	rpc := &fakeRPC{answer: func(method string, _ map[string]any) (json.RawMessage, error) {
		return json.Marshal(map[string]any{"type": "harness_run", "run": run})
	}}
	_, host := serveConsole(t, rpc)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	ws, _, err := dialBridge(ctx, host, testToken, "http://"+host)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.CloseNow() }()
	ws.SetReadLimit(1 << 24)
	s := harnesstest.NewSession(t, wsTransport{ctx: ctx, ws: ws})
	if path := harnesstest.HitBreakpoint(t, s, r.Launch); path != "/src/main.go" {
		t.Fatalf("frame source %q", path)
	}
	// A second bridge from the same console joins the lease (a child
	// session would); an editor dialling the port directly is refused.
	ws2, _, err := dialBridge(ctx, host, testToken, "http://"+host)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws2.CloseNow() }()
	editor := harnesstest.NewSession(t, harnesstest.DialDAP(t, r.HostPort))
	resp, err := editor.Request("initialize", map[string]any{"clientID": "vscode", "clientName": "Visual Studio Code"})
	if err != nil || resp["success"] != false {
		t.Fatalf("an editor got in while the console held the debugger: %v %v", resp, err)
	}
}

// TestNodeBreakpointThroughEditorPort drives a real Node debug run the way
// VS Code would with the shown launch.json: every connection goes to the
// console's editor port, which adds the lease line, points js-debug's child
// session at itself and maps the editor's workspace folder to /src.
func TestNodeBreakpointThroughEditorPort(t *testing.T) {
	r := harnesstest.UpNodeDebugRun(t)
	run := harness.Run{
		ID: "r1", Session: "s1", Scenario: "dbg", State: harness.StateReady, Debug: true, DebugLaunch: &r.Launch,
		Ports: []harness.RunPort{{Name: harness.RelayDAP, Service: harness.ServiceAdapter, HostPort: r.HostPort}},
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
	// VS Code fills ${workspaceFolder} and drops debugServer from what it
	// sends.
	const ws = "/home/me/checkout"
	args := map[string]any{}
	for k, val := range v.Editor {
		args[k] = val
	}
	args[harness.LocalRootKey] = ws
	delete(args, "debugServer")
	dial := func() harnesstest.Transport { return harnesstest.DialDAP(t, v.EditorPort) }
	path, _ := harnesstest.HitTSBreakpoint(t, dial, "attach", args, ws+"/src/app.ts")
	if path != ws+"/src/app.ts" {
		t.Fatalf("frame source %q, want the editor's %s/src/app.ts", path, ws)
	}
}
