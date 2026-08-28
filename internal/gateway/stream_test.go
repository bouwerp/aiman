package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/server"
	"github.com/coder/websocket"
)

func TestEventsWebSocket(t *testing.T) {
	sock := startEchoUnix(t, func(conn net.Conn, line []byte) {
		var req server.Request
		_ = json.Unmarshal(line, &req)
		out, _ := server.EncodeResponse(server.Response{
			ID:     req.ID,
			Result: map[string]any{"type": "events_attached"},
		})
		_, _ = conn.Write(out)
		ev := server.SessionEvent{Type: "activity", ID: "impl", Title: "working"}
		body, _ := json.Marshal(ev)
		_, _ = conn.Write(append(body, '\n'))
	})
	ts := httptest.NewServer((&Server{
		Socket: sock,
		Auth:   Auth{Token: "secret", Funnel: true},
	}).Handler())
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, strings.Replace(ts.URL, "http", "ws", 1)+"/v1/events", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer secret"}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")
	_, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), `"impl"`) {
		t.Fatalf("event %s", data)
	}
}

func TestTerminalWebSocket(t *testing.T) {
	sock := startEchoUnix(t, func(conn net.Conn, line []byte) {
		var req server.Request
		_ = json.Unmarshal(line, &req)
		out, _ := server.EncodeResponse(server.Response{
			ID:     req.ID,
			Result: map[string]any{"type": "attached"},
		})
		_, _ = conn.Write(out)
		_, _ = conn.Write([]byte("hello-pty"))
		buf := make([]byte, 64)
		_, _ = io.ReadAtLeast(conn, buf, 5)
	})
	ts := httptest.NewServer((&Server{
		Socket: sock,
		Auth:   Auth{Token: "secret", Funnel: true},
	}).Handler())
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, strings.Replace(ts.URL, "http", "ws", 1)+"/v1/sessions/s1/terminal?cols=80&rows=24", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer secret"}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")
	_, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "hello-pty" {
		t.Fatalf("output %q", data)
	}
	if err := ws.Write(ctx, websocket.MessageText, []byte(`{"type":"input","data":"ls"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
}
