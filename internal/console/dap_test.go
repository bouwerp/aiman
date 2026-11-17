package console

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/bouwerp/aiman/internal/harness/edge"
	"github.com/bouwerp/aiman/internal/usecase"
)

// directForwarder "forwards" each port to itself: the test's DAP server and
// a Docker run's edge listen on this machine's 127.0.0.1.
type directForwarder struct{}

func (directForwarder) Open(_ context.Context, ports []usecase.HarnessPort) (Forwards, error) {
	fw := &fakeForwards{died: make(chan usecase.HarnessForward, len(ports))}
	for _, p := range ports {
		fw.fwds = append(fw.fwds, usecase.HarnessForward{Name: p.Name, LocalPort: p.HostPort, HostPort: p.HostPort})
	}
	return fw, nil
}

func debugRunJSON(dapPort int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"type":"harness_run","run":{"id":"r1","session":"s1","scenario":"refund","sha":"a1b2c3","source":"file","state":"ready","debug":true,
"debug_launch":{"lang":"go","adapter":"dlv dap","arguments":{"request":"launch","mode":"exec","program":"/out/app","args":["serve"],"cwd":"/src"},"source_root":"/src"},
"ports":[{"name":"dap","service":"app","host_port":%d}],"steps":[],"checks":[]}}`, dapPort))
}

// echoDAP is a DAP server that records the first line each connection sends
// and answers every request with success.
type echoDAP struct {
	addr  string
	mu    sync.Mutex
	heads []string
}

func newEchoDAP(t *testing.T) *echoDAP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	d := &echoDAP{addr: ln.Addr().String()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				br := bufio.NewReader(c)
				head, err := br.ReadString('\n')
				if err != nil {
					return
				}
				d.mu.Lock()
				d.heads = append(d.heads, head)
				d.mu.Unlock()
				for {
					body, err := edge.ReadDAPMessage(br, edge.MaxDAPMessage)
					if err != nil {
						return
					}
					var req map[string]any
					_ = json.Unmarshal(body, &req)
					_ = edge.WriteDAPMessage(c, map[string]any{"type": "response", "request_seq": req["seq"], "success": true, "command": req["command"]})
				}
			}()
		}
	}()
	return d
}

func (d *echoDAP) port() int {
	_, p, _ := net.SplitHostPort(d.addr)
	var n int
	_, _ = fmt.Sscan(p, &n)
	return n
}

// serveConsole runs the console's handler on its own listener and polls once.
func serveConsole(t *testing.T, rpc RPC) (*Server, string) {
	t.Helper()
	s, host := newTestServer(t, rpc, directForwarder{}, nil)
	hs := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = hs.Serve(s.ln) }()
	t.Cleanup(func() { _ = hs.Close() })
	s.refresh(context.Background())
	return s, host
}

func dialBridge(ctx context.Context, host, token, origin string) (*websocket.Conn, *http.Response, error) {
	h := http.Header{}
	if origin != "" {
		h.Set("Origin", origin)
	}
	protos := []string{DAPSubprotocol}
	if token != "" {
		protos = append(protos, tokenSubprotocol+token)
	}
	return websocket.Dial(ctx, "ws://"+host+DAPPath, &websocket.DialOptions{Subprotocols: protos, HTTPHeader: h})
}

func TestDAPBridge(t *testing.T) {
	dap := newEchoDAP(t)
	rpc := &fakeRPC{answer: func(method string, params map[string]any) (json.RawMessage, error) {
		switch method {
		case "harness.status":
			return debugRunJSON(dap.port()), nil
		case "harness.debug":
			return json.RawMessage(`{"type":"harness_debug","debug":{"run":"r1","holder":null,"host_port":1,"refused":0}}`), nil
		}
		return nil, fmt.Errorf("unexpected %s", method)
	}}
	s, host := serveConsole(t, rpc)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	origin := "http://" + host

	for name, c := range map[string]struct{ token, origin string }{
		"no token":     {"", origin},
		"wrong token":  {"nope", origin},
		"no origin":    {testToken, ""},
		"other origin": {testToken, "http://evil.example"},
	} {
		if ws, resp, err := dialBridge(ctx, host, c.token, c.origin); err == nil {
			_ = ws.CloseNow()
			t.Fatalf("%s: the bridge accepted", name)
		} else if resp == nil || (resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden) {
			t.Fatalf("%s: %v %v", name, resp, err)
		}
	}

	ws, _, err := dialBridge(ctx, host, testToken, origin)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.CloseNow() }()
	if ws.Subprotocol() != DAPSubprotocol {
		t.Fatalf("subprotocol %q", ws.Subprotocol())
	}
	if err := ws.Write(ctx, websocket.MessageText, []byte(`{"seq":1,"type":"request","command":"initialize","arguments":{"clientName":"aiman harness console"}}`)); err != nil {
		t.Fatal(err)
	}
	_, msg, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var resp map[string]any
	if err := json.Unmarshal(msg, &resp); err != nil || resp["success"] != true || resp["command"] != "initialize" {
		t.Fatalf("reply %s: %v", msg, err)
	}
	dap.mu.Lock()
	head := dap.heads[0]
	dap.mu.Unlock()
	if head != edge.LeasePrefix+s.leaseToken+"\n" {
		t.Fatalf("first line %q, want the console's lease line", head)
	}
	// A second bridge (a child session) carries the same lease.
	ws2, _, err := dialBridge(ctx, host, testToken, origin)
	if err != nil {
		t.Fatal(err)
	}
	_ = ws2.Write(ctx, websocket.MessageText, []byte(`{"seq":1,"type":"request","command":"initialize"}`))
	if _, _, err := ws2.Read(ctx); err != nil {
		t.Fatal(err)
	}
	dap.mu.Lock()
	if len(dap.heads) != 2 || dap.heads[1] != dap.heads[0] {
		t.Fatalf("heads %q", dap.heads)
	}
	dap.mu.Unlock()
	_ = ws2.CloseNow()

	// Something that is not DAP JSON ends the bridge.
	_ = ws.Write(ctx, websocket.MessageText, []byte("not json"))
	if _, _, err := ws.Read(ctx); err == nil {
		t.Fatal("the bridge kept a page that sent garbage")
	}
}

func TestDebugAPI(t *testing.T) {
	dap := newEchoDAP(t)
	rpc := &fakeRPC{answer: func(method string, params map[string]any) (json.RawMessage, error) {
		switch method {
		case "harness.status":
			return debugRunJSON(dap.port()), nil
		case "harness.debug":
			return json.RawMessage(`{"type":"harness_debug","debug":{"run":"r1","holder":{"client_name":"vscode","remote":"x","since":"2026-11-17T12:00:00Z","connections":1},"host_port":1,"refused":0}}`), nil
		case "harness.files":
			return json.RawMessage(`{"type":"harness_files","run":"r1","root":"/src","files":["main.go"],"truncated":false}`), nil
		case "harness.file":
			return json.RawMessage(`{"type":"harness_file","run":"r1","path":"main.go","size":5,"offset":0,"data":"hello","eof":true,"binary":false}`), nil
		}
		return nil, fmt.Errorf("unexpected %s", method)
	}}
	s, host := newTestServer(t, rpc, directForwarder{}, nil)
	s.refresh(context.Background())

	w := do(t, s, http.MethodGet, host, "/api/debug?run=r1", testToken, "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("debug %d %s", w.Code, w.Body)
	}
	var v struct {
		Debug     map[string]any `json:"debug"`
		LocalPort int            `json:"local_port"`
		Editor    map[string]any `json:"editor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.LocalPort != dap.port() || v.Editor["port"] != float64(dap.port()) || v.Editor["program"] != "/out/app" {
		t.Fatalf("debug view %+v", v)
	}
	if got := rpc.callsOf("harness.debug"); len(got) != 1 || got[0]["op"] != "status" {
		t.Fatalf("calls %v", got)
	}

	origin := "http://" + host
	if w := do(t, s, http.MethodPost, host, DebugOpPath, testToken, origin, `{"run":"r1","op":"take"}`); w.Code != http.StatusOK {
		t.Fatalf("take %d %s", w.Code, w.Body)
	}
	if got := rpc.callsOf("harness.debug"); got[1]["op"] != "take" || got[1]["run"] != "r1" {
		t.Fatalf("take call %v", got[1])
	}
	if w := do(t, s, http.MethodPost, host, DebugOpPath, testToken, "", `{"run":"r1","op":"take"}`); w.Code != http.StatusForbidden {
		t.Fatalf("take without origin %d", w.Code)
	}
	if w := do(t, s, http.MethodPost, host, DebugOpPath, testToken, origin, `{"run":"r1","op":"pause"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad op %d", w.Code)
	}
	if w := do(t, s, http.MethodGet, host, "/api/debug?run=r9", testToken, "", ""); w.Code != http.StatusConflict {
		t.Fatalf("other run %d", w.Code)
	}

	if w := do(t, s, http.MethodGet, host, "/api/files?run=r1", testToken, "", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "main.go") {
		t.Fatalf("files %d %s", w.Code, w.Body)
	}
	if w := do(t, s, http.MethodGet, host, "/api/file?run=r1&path=/src/main.go&offset=0", testToken, "", ""); w.Code != http.StatusOK {
		t.Fatalf("file %d %s", w.Code, w.Body)
	}
	if got := rpc.callsOf("harness.file"); got[0]["path"] != "/src/main.go" || got[0]["run"] != "r1" {
		t.Fatalf("file call %v", got)
	}
	if w := do(t, s, http.MethodGet, host, "/api/file?run=r1", testToken, "", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("file without path %d", w.Code)
	}
	csp := do(t, s, http.MethodGet, host, "/", "", "", "").Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "connect-src 'self' ws://"+host) {
		t.Fatalf("csp %q", csp)
	}
}
