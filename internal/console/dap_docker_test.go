package console

import (
	"context"
	"encoding/json"
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
	_, host := serveConsole(t, rpc, directForwarder{})
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
