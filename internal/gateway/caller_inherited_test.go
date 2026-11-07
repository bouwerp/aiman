package gateway

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/server"
	"github.com/coder/websocket"
)

// startCallerSocket answers every request and sends each request's caller on
// the returned channel.
func startCallerSocket(t *testing.T) (string, <-chan string) {
	t.Helper()
	callers := make(chan string, 4)
	sock := startEchoUnix(t, func(conn net.Conn, line []byte) {
		var req server.Request
		if err := json.Unmarshal(line, &req); err != nil {
			t.Errorf("unmarshal: %v", err)
			return
		}
		callers <- req.Caller
		out, _ := server.EncodeResponse(server.Response{ID: req.ID, Result: map[string]any{"type": "ok"}})
		_, _ = conn.Write(out)
	})
	return sock, callers
}

func receiveCaller(t *testing.T, callers <-chan string) string {
	t.Helper()
	select {
	case got := <-callers:
		return got
	case <-time.After(3 * time.Second):
		t.Fatal("request never reached serve")
		return ""
	}
}

func TestTerminalNeverSendsInheritedCaller(t *testing.T) {
	t.Setenv("AIMAN_ID", "inherited-session")
	sock, callers := startCallerSocket(t)
	ts := httptest.NewServer((&Server{Socket: sock, Auth: Auth{Token: "secret", Funnel: true}}).Handler())
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, strings.Replace(ts.URL, "http", "ws", 1)+"/v1/sessions/s1/terminal", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer secret"}},
	})
	if err != nil {
		t.Fatalf("dial: %v (%v)", err, resp)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")

	if got := receiveCaller(t, callers); got != "" {
		t.Fatalf("pty.attach reached serve with caller %q", got)
	}
}

// A session's own aiman CLI goes through the package functions and still
// identifies itself.
func TestPackageCallsKeepEnvCaller(t *testing.T) {
	t.Setenv("AIMAN_ID", "the-session")
	sock, callers := startCallerSocket(t)

	if _, err := server.CallRaw(sock, "session.list", json.RawMessage("{}")); err != nil {
		t.Fatalf("CallRaw: %v", err)
	}
	if got := receiveCaller(t, callers); got != "the-session" {
		t.Fatalf("CallRaw caller %q, want the-session", got)
	}
	stream, err := server.EventsDial(sock)
	if err != nil {
		t.Fatalf("EventsDial: %v", err)
	}
	defer stream.Close()
	if got := receiveCaller(t, callers); got != "the-session" {
		t.Fatalf("EventsDial caller %q, want the-session", got)
	}
	att, err := server.AttachDial(sock, "s1", 80, 24)
	if err != nil {
		t.Fatalf("AttachDial: %v", err)
	}
	defer att.Close()
	if got := receiveCaller(t, callers); got != "the-session" {
		t.Fatalf("AttachDial caller %q, want the-session", got)
	}
}
