package gateway

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/server"
	"github.com/coder/websocket"
)

// callerRecorder is a fake serve socket that records the caller of every
// request it receives and answers each one with a minimal success.
type callerRecorder struct {
	mu      sync.Mutex
	callers map[string]string // method -> caller
}

func (c *callerRecorder) start(t *testing.T) string {
	t.Helper()
	c.callers = map[string]string{}
	return startEchoUnix(t, func(conn net.Conn, line []byte) {
		var req server.Request
		if err := json.Unmarshal(line, &req); err != nil {
			t.Errorf("unmarshal: %v", err)
			return
		}
		c.mu.Lock()
		c.callers[req.Method] = req.Caller
		c.mu.Unlock()
		result := map[string]any{"type": "ok"}
		if req.Method == "notify.since" {
			result = map[string]any{"type": "notifications", "entries": []any{}, "next": 0}
		}
		out, _ := server.EncodeResponse(server.Response{ID: req.ID, Result: result})
		_, _ = conn.Write(out)
	})
}

func (c *callerRecorder) caller(method string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.callers[method]
	return v, ok
}

// A gateway started from inside a session's terminal inherits AIMAN_ID. Phone
// calls must still reach serve as the operator, never as that session.
func TestGatewayNeverSendsInheritedCaller(t *testing.T) {
	t.Setenv("AIMAN_ID", "inherited-session")
	rec := &callerRecorder{}
	sock := rec.start(t)
	h := (&Server{Socket: sock, Auth: Auth{Token: "secret", Funnel: true}}).Handler()

	requests := []struct {
		method, verb, path, body string
	}{
		{"session.restart", http.MethodPost, "/v1/rpc", `{"method":"session.restart","params":{"session":"x"}}`},
		{"session.list", http.MethodGet, "/v1/sessions", ""},
		{"session.get", http.MethodGet, "/v1/sessions/impl", ""},
	}
	for _, r := range requests {
		req := httptest.NewRequest(r.verb, r.path, strings.NewReader(r.body))
		req.Header.Set("Authorization", "Bearer secret")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	if _, _, err := (SocketSource{Socket: sock}).Since(context.Background(), 0, 10); err != nil {
		t.Fatalf("notify.since: %v", err)
	}
	dialEvents(t, h)

	for _, method := range []string{"session.restart", "session.list", "session.get", "notify.since", "session.events"} {
		got, ok := rec.caller(method)
		if !ok {
			t.Fatalf("%s never reached serve", method)
		}
		if got != "" {
			t.Errorf("%s reached serve with caller %q; the gateway must not send a caller taken from its environment", method, got)
		}
	}
}

// dialEvents opens /v1/events once so the gateway sends session.events.
func dialEvents(t *testing.T, h http.Handler) {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, strings.Replace(ts.URL, "http", "ws", 1)+"/v1/events", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer secret"}},
	})
	if err != nil {
		t.Fatalf("dial events: %v", err)
	}
	_ = ws.Close(websocket.StatusNormalClosure, "")
}
