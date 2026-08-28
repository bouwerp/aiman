package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/server"
)

func TestHealthIsUnauthenticated(t *testing.T) {
	h := (&Server{Auth: Auth{Token: "secret", Funnel: true}}).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.Bytes())
	}
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestRPCRequiresAuth(t *testing.T) {
	h := (&Server{Auth: Auth{Token: "secret", Funnel: true}}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/rpc", strings.NewReader(`{"method":"ping"}`))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code %d", rec.Code)
	}
}

func TestRPCProxiesPing(t *testing.T) {
	sock := startEchoUnix(t, func(conn net.Conn, line []byte) {
		var req server.Request
		if err := json.Unmarshal(line, &req); err != nil {
			t.Errorf("unmarshal: %v", err)
			return
		}
		if req.Method != "ping" {
			t.Errorf("method %s", req.Method)
		}
		out, _ := server.EncodeResponse(server.Response{
			ID:     req.ID,
			Result: map[string]any{"type": "pong", "protocol": 1},
		})
		_, _ = conn.Write(out)
	})
	h := (&Server{
		Socket: sock,
		Auth:   Auth{Token: "secret", Funnel: true},
	}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/rpc", strings.NewReader(`{"method":"ping"}`))
	req.Header.Set("Authorization", "Bearer secret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"pong"`) {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestRPCRejectsStreamingMethods(t *testing.T) {
	h := (&Server{Auth: Auth{Token: "secret", Funnel: true}}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/rpc", strings.NewReader(`{"method":"pty.attach"}`))
	req.Header.Set("Authorization", "Bearer secret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code %d", rec.Code)
	}
}

func TestListSessionsREST(t *testing.T) {
	sock := startEchoUnix(t, func(conn net.Conn, line []byte) {
		var req server.Request
		_ = json.Unmarshal(line, &req)
		if req.Method != "session.list" {
			t.Errorf("method %s", req.Method)
		}
		out, _ := server.EncodeResponse(server.Response{
			ID:     req.ID,
			Result: map[string]any{"type": "session_list", "sessions": []any{}},
		})
		_, _ = conn.Write(out)
	})
	h := (&Server{Socket: sock, Auth: Auth{Token: "secret", Funnel: true}}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer secret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "session_list") {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestGetSessionREST(t *testing.T) {
	sock := startEchoUnix(t, func(conn net.Conn, line []byte) {
		var req server.Request
		_ = json.Unmarshal(line, &req)
		var params struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(req.Params, &params)
		if req.Method != "session.get" || params.ID != "impl" {
			t.Errorf("method %s id %s", req.Method, params.ID)
		}
		out, _ := server.EncodeResponse(server.Response{
			ID:     req.ID,
			Result: map[string]any{"type": "session", "name": "impl"},
		})
		_, _ = conn.Write(out)
	})
	h := (&Server{Socket: sock, Auth: Auth{Token: "secret", Funnel: true}}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/impl", nil)
	req.Header.Set("Authorization", "Bearer secret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
}

func TestTailnetWhoIsOnRPC(t *testing.T) {
	sock := startEchoUnix(t, func(conn net.Conn, line []byte) {
		var req server.Request
		_ = json.Unmarshal(line, &req)
		out, _ := server.EncodeResponse(server.Response{ID: req.ID, Result: map[string]any{"type": "pong"}})
		_, _ = conn.Write(out)
	})
	h := (&Server{
		Socket: sock,
		Auth: Auth{
			Token: "secret",
			Allow: []string{"me@example.com"},
			WhoIs: func(context.Context, string) (string, error) { return "me@example.com", nil },
		},
	}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/rpc", strings.NewReader(`{"method":"ping"}`))
	req.Header.Set("Authorization", "Bearer secret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
}

func startEchoUnix(t *testing.T, handle func(net.Conn, []byte)) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ag")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "aiman.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				line, err := bufio.NewReader(conn).ReadBytes('\n')
				if err != nil {
					return
				}
				handle(conn, line)
			}(c)
		}
	}()
	return sock
}
