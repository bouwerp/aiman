package console

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/edge"
)

// fakeJSDebug answers like js-debug's DAP server: the root session's attach
// asks for a child with startDebugging; a child session's attach (with the
// pending target) stops at the first breakpoint it is given.
type fakeJSDebug struct {
	addr   string
	mu     sync.Mutex
	leases []string
	bps    []string
}

func newFakeJSDebug(t *testing.T) *fakeJSDebug {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	d := &fakeJSDebug{addr: ln.Addr().String()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go d.serve(c)
		}
	}()
	return d
}

func (d *fakeJSDebug) port() int {
	_, p, _ := net.SplitHostPort(d.addr)
	n, _ := strconv.Atoi(p)
	return n
}

func (d *fakeJSDebug) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	br := bufio.NewReader(c)
	head, err := br.ReadString('\n')
	if err != nil {
		return
	}
	d.mu.Lock()
	d.leases = append(d.leases, strings.TrimSpace(strings.TrimPrefix(head, edge.LeasePrefix)))
	d.mu.Unlock()
	seq := 1000
	send := func(m map[string]any) {
		seq++
		m["seq"] = seq
		_ = edge.WriteDAPMessage(c, m)
	}
	for {
		body, err := edge.ReadDAPMessage(br, edge.MaxDAPMessage)
		if err != nil {
			return
		}
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		if req["type"] != "request" {
			continue
		}
		send(map[string]any{"type": "response", "request_seq": req["seq"], "success": true, "command": req["command"]})
		args, _ := req["arguments"].(map[string]any)
		switch req["command"] {
		case "attach":
			if args["__pendingTargetId"] == nil {
				send(map[string]any{"type": "request", "command": "startDebugging", "arguments": map[string]any{
					"request": "attach", "configuration": map[string]any{"type": "pwa-node", "name": "app.js [1]", "__pendingTargetId": "t1"},
				}})
			}
		case "setBreakpoints":
			src, _ := args["source"].(map[string]any)
			d.mu.Lock()
			d.bps = append(d.bps, fmt.Sprint(src["path"]))
			d.mu.Unlock()
			send(map[string]any{"type": "event", "event": "stopped", "body": map[string]any{
				"reason": "breakpoint", "threadId": 1,
				"source": map[string]any{"path": "/src/src/app.ts"},
			}})
		}
	}
}

// editorConn is a DAP client standing in for VS Code.
type editorConn struct {
	t   *testing.T
	c   net.Conn
	br  *bufio.Reader
	seq int
}

func dialEditor(t *testing.T, port int) *editorConn {
	t.Helper()
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return &editorConn{t: t, c: c, br: bufio.NewReader(c)}
}

func (e *editorConn) send(command string, args any) {
	e.t.Helper()
	e.seq++
	if err := edge.WriteDAPMessage(e.c, map[string]any{"seq": e.seq, "type": "request", "command": command, "arguments": args}); err != nil {
		e.t.Fatal(err)
	}
}

// next reads until a message matches.
func (e *editorConn) next(match func(map[string]any) bool) map[string]any {
	e.t.Helper()
	_ = e.c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		body, err := edge.ReadDAPMessage(e.br, edge.MaxDAPMessage)
		if err != nil {
			e.t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			e.t.Fatal(err)
		}
		if match(m) {
			return m
		}
	}
}

func nodeRunJSON(dapPort int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"type":"harness_run","run":{"id":"r1","session":"s1","scenario":"refund","sha":"a1b2c3","source":"file","state":"ready","debug":true,
"debug_launch":{"lang":"node","adapter":"js-debug","request":"attach","children":true,"arguments":{"type":"pwa-node","request":"attach","address":"10.232.1.2","port":9229,"cwd":"/src"},"source_root":"/src"},
"ports":[{"name":"dap","service":"debug-adapter","host_port":%d}],"steps":[],"checks":[]}}`, dapPort))
}

func TestStartDebuggingOpensChild(t *testing.T) {
	dap := newFakeJSDebug(t)
	rpc := &fakeRPC{answer: func(method string, _ map[string]any) (json.RawMessage, error) {
		switch method {
		case "harness.status":
			return nodeRunJSON(dap.port()), nil
		case "harness.debug":
			return json.RawMessage(`{"type":"harness_debug","debug":{"run":"r1","holder":null,"host_port":1,"refused":0}}`), nil
		}
		return nil, fmt.Errorf("unexpected %s", method)
	}}
	s, host := newTestServer(t, rpc, directForwarder{}, nil)
	t.Cleanup(s.closeEditor)
	s.refresh(context.Background())

	w := do(t, s, http.MethodGet, host, "/api/debug?run=r1", testToken, "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("debug %d %s", w.Code, w.Body)
	}
	var v struct {
		LocalPort  int            `json:"local_port"`
		EditorPort int            `json:"editor_port"`
		Editor     map[string]any `json:"editor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.EditorPort == 0 || v.EditorPort == v.LocalPort || v.Editor["debugServer"] != float64(v.EditorPort) ||
		v.Editor["type"] != "pwa-node" || v.Editor[harness.LocalRootKey] != "${workspaceFolder}" {
		t.Fatalf("debug view %+v", v)
	}

	// VS Code attaches through the editor port with its workspace folder
	// filled in.
	const ws = "/home/me/repo"
	root := dialEditor(t, v.EditorPort)
	root.send("initialize", map[string]any{"clientID": "vscode", "supportsStartDebuggingRequest": true})
	attach := map[string]any{"type": "pwa-node", "request": "attach", "debugServer": v.EditorPort, harness.LocalRootKey: ws}
	root.send("attach", attach)
	sd := root.next(func(m map[string]any) bool { return m["type"] == "request" && m["command"] == "startDebugging" })
	cfg := sd["arguments"].(map[string]any)["configuration"].(map[string]any)
	if cfg["debugServer"] != float64(v.EditorPort) || cfg[harness.LocalRootKey] != ws || cfg["__pendingTargetId"] != "t1" {
		t.Fatalf("startDebugging configuration %v: want debugServer and the workspace folder added", cfg)
	}

	// VS Code opens the child at debugServer with that configuration.
	child := dialEditor(t, v.EditorPort)
	child.send("initialize", map[string]any{"clientID": "vscode"})
	child.send("attach", cfg)
	child.send("setBreakpoints", map[string]any{"source": map[string]any{"path": ws + "/src/app.ts"}, "breakpoints": []any{map[string]any{"line": 9}}})
	stopped := child.next(func(m map[string]any) bool { return m["type"] == "event" && m["event"] == "stopped" })
	if p := stopped["body"].(map[string]any)["source"].(map[string]any)["path"]; p != ws+"/src/app.ts" {
		t.Fatalf("stopped source %v, want the editor's path", p)
	}

	dap.mu.Lock()
	defer dap.mu.Unlock()
	if len(dap.bps) != 1 || dap.bps[0] != "/src/src/app.ts" {
		t.Fatalf("adapter got breakpoints at %v, want /src/src/app.ts", dap.bps)
	}
	// Both connections carry one lease token, the editor's own.
	if len(dap.leases) != 2 || dap.leases[0] != dap.leases[1] || dap.leases[0] == s.leaseToken || dap.leases[0] == "" {
		t.Fatalf("leases %q (page lease %q)", dap.leases, s.leaseToken)
	}
}
