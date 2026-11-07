package gateway

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/coder/websocket"
)

// A paired phone without allow_destructive must not reach a shell: typing into
// a terminal, starting or restarting agents, and the terminal WebSocket all
// need the flag. Prompts and steering stay plain.
func TestDeviceRPCDestructiveScope(t *testing.T) {
	tests := []struct {
		method string
		plain  bool
	}{
		{"pty.input", false},
		{"pty.create", false},
		{"pty.run", false},
		{"session.create", false},
		{"session.restart", false},
		{"session.prompt", true},
		{"steer.send", true},
	}
	for _, tt := range tests {
		for _, destructive := range []bool{false, true} {
			name := tt.method + map[bool]string{false: "/plain-device", true: "/destructive-device"}[destructive]
			t.Run(name, func(t *testing.T) {
				rig := newDeviceRig(t, nil)
				_, tok := rig.device(t, destructive)
				rec := rig.do(http.MethodPost, "/v1/rpc", tok, `{"method":"`+tt.method+`","params":{"id":"s1"}}`)
				want := http.StatusOK
				if !tt.plain && !destructive {
					want = http.StatusForbidden
				}
				if rec.Code != want {
					t.Fatalf("code %d want %d body %s", rec.Code, want, rec.Body.String())
				}
				if forwarded := rig.forwards.Load() > 0; forwarded != (want == http.StatusOK) {
					t.Fatalf("forwarded=%v", forwarded)
				}
			})
		}
	}
}

func dialTerminal(t *testing.T, h http.Handler, tok string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	url := strings.Replace(ts.URL, "http", "ws", 1) + "/v1/sessions/s1/terminal"
	return websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + tok}},
	})
}

func TestDeviceTerminalNeedsDestructive(t *testing.T) {
	rig := newDeviceRig(t, nil)
	_, plain := rig.device(t, false)
	ws, resp, err := dialTerminal(t, rig.h, plain)
	if err == nil {
		_ = ws.Close(websocket.StatusNormalClosure, "")
		t.Fatal("device without allow_destructive opened the terminal")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("resp %+v err %v, want 403", resp, err)
	}
	if rig.forwards.Load() != 0 {
		t.Fatal("refused terminal reached serve")
	}

	_, destructive := rig.device(t, true)
	ws, resp, err = dialTerminal(t, rig.h, destructive)
	if err != nil {
		t.Fatalf("device with allow_destructive: %v", err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status %d", resp.StatusCode)
	}
	deadline := time.Now().Add(2 * time.Second)
	for rig.forwards.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if rig.forwards.Load() == 0 {
		t.Fatal("allowed terminal never reached serve")
	}
}

// gateServe is a fake serve whose session s1 holds a design gate (t1) and a
// plain item (t2). It records every method forwarded to it.
type gateServe struct {
	mu      sync.Mutex
	methods []string
}

func (g *gateServe) start(t *testing.T) string {
	t.Helper()
	list := domain.TodoList{SessionID: "s1", Items: []domain.TodoItem{
		{ID: "t1", Text: "Design approved", State: domain.TodoPending, Ref: "gate:design"},
		{ID: "t2", Text: "write tests", State: domain.TodoPending},
	}}
	return startEchoUnix(t, func(conn net.Conn, line []byte) {
		var req server.Request
		_ = json.Unmarshal(line, &req)
		g.mu.Lock()
		g.methods = append(g.methods, req.Method)
		g.mu.Unlock()
		result := map[string]any{"type": "ok", "method": req.Method}
		if req.Method == "todo.get" {
			result = map[string]any{"type": "todo_list", "list": list}
		}
		out, _ := server.EncodeResponse(server.Response{ID: req.ID, Result: result})
		_, _ = conn.Write(out)
	})
}

func (g *gateServe) forwarded(method string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Contains(g.methods, method)
}

// Closing, dropping or editing a gate from a phone is an approval and needs
// allow_destructive; other to-do edits stay plain.
func TestDeviceGateTodoNeedsDestructive(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		gate        bool
		destructive bool
	}{
		{"close gate", `{"method":"todo.update","params":{"session":"s1","id":"t1","state":"done"}}`, true, false},
		{"rename gate", `{"method":"todo.update","params":{"session":"s1","id":"t1","text":"ok"}}`, true, false},
		{"drop gate", `{"method":"todo.update","params":{"session":"s1","id":"t1","state":"dropped"}}`, true, false},
		{"remove gate", `{"method":"todo.remove","params":{"session":"s1","id":"t1"}}`, true, false},
		{"close gate with flag", `{"method":"todo.update","params":{"session":"s1","id":"t1","state":"done"}}`, true, true},
		{"remove gate with flag", `{"method":"todo.remove","params":{"session":"s1","id":"t1"}}`, true, true},
		{"close plain item", `{"method":"todo.update","params":{"session":"s1","id":"t2","state":"done"}}`, false, false},
		{"remove plain item", `{"method":"todo.remove","params":{"session":"s1","id":"t2"}}`, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := newDeviceRig(t, nil)
			fake := &gateServe{}
			rig.srv.Socket = fake.start(t)
			_, tok := rig.device(t, tt.destructive)
			rec := rig.do(http.MethodPost, "/v1/rpc", tok, tt.body)
			var method struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal([]byte(tt.body), &method)
			refused := tt.gate && !tt.destructive
			if refused {
				if rec.Code != http.StatusForbidden || fake.forwarded(method.Method) {
					t.Fatalf("code %d forwarded=%v body %s, want 403 and not forwarded",
						rec.Code, fake.forwarded(method.Method), rec.Body.String())
				}
				return
			}
			if rec.Code != http.StatusOK || !fake.forwarded(method.Method) {
				t.Fatalf("code %d forwarded=%v body %s", rec.Code, fake.forwarded(method.Method), rec.Body.String())
			}
		})
	}
}
