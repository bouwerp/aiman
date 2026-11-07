package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/server"
	"github.com/coder/websocket"
)

const hostToken = "secret"

type deviceRig struct {
	srv      *Server
	h        http.Handler
	devices  *DeviceStore
	codes    *PairCodes
	forwards *atomic.Int32
}

func newDeviceRig(t *testing.T, mut func(*Auth)) *deviceRig {
	t.Helper()
	var forwards atomic.Int32
	sock := startEchoUnix(t, func(conn net.Conn, line []byte) {
		forwards.Add(1)
		var req server.Request
		_ = json.Unmarshal(line, &req)
		out, _ := server.EncodeResponse(server.Response{ID: req.ID, Result: map[string]any{"type": "ok", "method": req.Method}})
		_, _ = conn.Write(out)
	})
	dir := t.TempDir()
	devices := NewDeviceStore(filepath.Join(dir, DevicesFileName))
	codes := NewPairCodes(filepath.Join(dir, PairCodesFileName))
	auth := Auth{
		Token:   hostToken,
		Devices: devices,
		WhoIs:   func(context.Context, string) (string, error) { return "me@example.com", nil },
	}
	if mut != nil {
		mut(&auth)
	}
	srv := &Server{Socket: sock, Auth: auth, Push: mustStore(t), PairCodes: codes}
	return &deviceRig{srv: srv, h: srv.Handler(), devices: devices, codes: codes, forwards: &forwards}
}

func (r *deviceRig) do(method, path, bearer, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	r.h.ServeHTTP(rec, req)
	return rec
}

func (r *deviceRig) device(t *testing.T, destructive bool) (Device, string) {
	t.Helper()
	dev, tok, err := r.devices.Create("phone", "me@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if destructive {
		if err := r.devices.SetAllowDestructive(dev.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	return dev, tok
}

func TestRPCAllowlistByToken(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		hostToken   bool
		destructive bool
		wantCode    int
	}{
		{"host may call anything", "context.put", true, false, http.StatusOK},
		{"host destructive", "pty.kill", true, false, http.StatusOK},
		{"device read", "session.list", false, false, http.StatusOK},
		{"device write", "session.prompt", false, false, http.StatusOK},
		{"device unlisted", "context.put", false, false, http.StatusForbidden},
		{"device unknown", "made.up", false, true, http.StatusForbidden},
		{"device destructive without flag", "pty.kill", false, false, http.StatusForbidden},
		{"device destructive with flag", "pty.kill", false, true, http.StatusOK},
		{"device push stays local", "push.register", false, false, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := newDeviceRig(t, nil)
			tok := hostToken
			if !tt.hostToken {
				_, tok = rig.device(t, tt.destructive)
			}
			rec := rig.do(http.MethodPost, "/v1/rpc", tok, `{"method":"`+tt.method+`"}`)
			if rec.Code != tt.wantCode {
				t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
			}
			forwarded := rig.forwards.Load() > 0
			if forwarded != (tt.wantCode == http.StatusOK) {
				t.Fatalf("forwarded=%v", forwarded)
			}
			if tt.wantCode == http.StatusForbidden {
				var resp server.Response
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
					t.Fatalf("body is not JSON: %s", rec.Body.String())
				}
				if resp.Error == nil || resp.Error.Code != "forbidden" || !strings.Contains(resp.Error.Message, tt.method) {
					t.Fatalf("error %+v", resp.Error)
				}
			}
		})
	}
}

func TestAuthTokens(t *testing.T) {
	tests := []struct {
		name     string
		token    func(t *testing.T, r *deviceRig) string
		whoErr   bool
		allow    []string
		wantCode int
	}{
		{"host", func(*testing.T, *deviceRig) string { return hostToken }, false, nil, http.StatusOK},
		{"device", func(t *testing.T, r *deviceRig) string { _, tok := r.device(t, false); return tok }, false, nil, http.StatusOK},
		{"none", func(*testing.T, *deviceRig) string { return "" }, false, nil, http.StatusUnauthorized},
		{"wrong", func(*testing.T, *deviceRig) string { return "nope" }, false, nil, http.StatusUnauthorized},
		{"revoked device", func(t *testing.T, r *deviceRig) string {
			dev, tok := r.device(t, false)
			if err := r.devices.Revoke(dev.ID); err != nil {
				t.Fatal(err)
			}
			return tok
		}, false, nil, http.StatusUnauthorized},
		{"device without tailnet identity", func(t *testing.T, r *deviceRig) string { _, tok := r.device(t, false); return tok }, true, nil, http.StatusForbidden},
		{"device with login not allowed", func(t *testing.T, r *deviceRig) string { _, tok := r.device(t, false); return tok }, false, []string{"other@example.com"}, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := newDeviceRig(t, func(a *Auth) {
				a.Allow = tt.allow
				if tt.whoErr {
					a.WhoIs = func(context.Context, string) (string, error) { return "", errors.New("no identity") }
				}
			})
			rec := rig.do(http.MethodPost, "/v1/rpc", tt.token(t, rig), `{"method":"ping"}`)
			if rec.Code != tt.wantCode {
				t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestQueryTokenOnlyOnWebSockets(t *testing.T) {
	rig := newDeviceRig(t, nil)
	_, tok := rig.device(t, true)
	rec := rig.do(http.MethodPost, "/v1/rpc?access_token="+tok, "", `{"method":"ping"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("rpc must ignore access_token: %d", rec.Code)
	}
	rec = rig.do(http.MethodGet, "/v1/sessions?access_token="+tok, "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("REST must ignore access_token: %d", rec.Code)
	}

	ts := httptest.NewServer(rig.h)
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	base := strings.Replace(ts.URL, "http", "ws", 1)
	for _, path := range []string{"/v1/events", "/v1/sessions/s1/terminal"} {
		ws, resp, err := websocket.Dial(ctx, base+path+"?access_token="+tok, nil)
		if err != nil {
			t.Fatalf("%s: dial with query token: %v", path, err)
		}
		_ = ws.Close(websocket.StatusNormalClosure, "")
		if resp.StatusCode != http.StatusSwitchingProtocols {
			t.Fatalf("%s: status %d", path, resp.StatusCode)
		}
		_, resp, err = websocket.Dial(ctx, base+path+"?access_token=wrong", nil)
		if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s: wrong query token accepted", path)
		}
	}
}

func TestPair(t *testing.T) {
	tests := []struct {
		name     string
		mut      func(*Auth)
		code     func(valid string) string
		wantCode int
	}{
		{"valid", nil, func(c string) string { return c }, http.StatusOK},
		{"funnel refuses", func(a *Auth) { a.Funnel = true; a.WhoIs = nil }, func(c string) string { return c }, http.StatusForbidden},
		{"no whois", func(a *Auth) { a.WhoIs = nil }, func(c string) string { return c }, http.StatusForbidden},
		{"whois error", func(a *Auth) {
			a.WhoIs = func(context.Context, string) (string, error) { return "", errors.New("x") }
		}, func(c string) string { return c }, http.StatusForbidden},
		{"login not allowed", func(a *Auth) { a.Allow = []string{"other@example.com"} }, func(c string) string { return c }, http.StatusForbidden},
		{"wrong code", nil, func(string) string { return "AAAAAAAAAA" }, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := newDeviceRig(t, tt.mut)
			code, _, err := rig.codes.Mint("from-cli")
			if err != nil {
				t.Fatal(err)
			}
			rec := rig.do(http.MethodPost, "/v1/pair", "", `{"code":"`+tt.code(code)+`","device_name":"Pieter's iPhone"}`)
			if rec.Code != tt.wantCode {
				t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
			}
			devs, err := rig.devices.List()
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantCode != http.StatusOK {
				if len(devs) != 0 {
					t.Fatalf("device created on refusal: %+v", devs)
				}
				return
			}
			var out struct {
				DeviceID string `json:"device_id"`
				Token    string `json:"token"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if len(devs) != 1 || devs[0].ID != out.DeviceID || devs[0].Login != "me@example.com" || devs[0].Name != "Pieter's iPhone" {
				t.Fatalf("devices %+v out %+v", devs, out)
			}
			if rec := rig.do(http.MethodPost, "/v1/rpc", out.Token, `{"method":"ping"}`); rec.Code != http.StatusOK {
				t.Fatalf("new token rejected: %d", rec.Code)
			}
			if rec := rig.do(http.MethodPost, "/v1/pair", "", `{"code":"`+code+`"}`); rec.Code != http.StatusUnauthorized {
				t.Fatalf("code reused: %d", rec.Code)
			}
		})
	}
}

func TestPairDeviceNameFallsBackToMintName(t *testing.T) {
	rig := newDeviceRig(t, nil)
	code, _, err := rig.codes.Mint("from-cli")
	if err != nil {
		t.Fatal(err)
	}
	if rec := rig.do(http.MethodPost, "/v1/pair", "", `{"code":"`+code+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	devs, _ := rig.devices.List()
	if len(devs) != 1 || devs[0].Name != "from-cli" {
		t.Fatalf("%+v", devs)
	}
}

func TestPairLockout(t *testing.T) {
	rig := newDeviceRig(t, nil)
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	rig.srv.now = func() time.Time { return now }
	code, _, err := rig.codes.Mint("")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		now = now.Add(time.Second)
		if rec := rig.do(http.MethodPost, "/v1/pair", "", `{"code":"BBBBBBBBBB"}`); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: code %d", i+1, rec.Code)
		}
	}
	// Five failures do not lock; the sixth does.
	now = now.Add(time.Second)
	if rec := rig.do(http.MethodPost, "/v1/pair", "", `{"code":"BBBBBBBBBB"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("sixth failure: code %d", rec.Code)
	}
	now = now.Add(time.Second)
	if rec := rig.do(http.MethodPost, "/v1/pair", "", `{"code":"`+code+`"}`); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("locked: code %d", rec.Code)
	}
	// The window rolls: a minute after the first failure only five remain.
	now = now.Add(time.Minute - 6*time.Second)
	if rec := rig.do(http.MethodPost, "/v1/pair", "", `{"code":"`+code+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("after the minute: code %d body %s", rec.Code, rec.Body.String())
	}
}

func TestDevicesRoutes(t *testing.T) {
	rig := newDeviceRig(t, nil)
	self, selfTok := rig.device(t, false)
	other, _ := rig.device(t, false)

	rec := rig.do(http.MethodGet, "/v1/devices", hostToken, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "sha256") || strings.Contains(rec.Body.String(), self.TokenHash) {
		t.Fatalf("listing leaks hashes: %s", rec.Body.String())
	}
	var list struct {
		Devices []DeviceInfo `json:"devices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Devices) != 2 {
		t.Fatalf("list %s err %v", rec.Body.String(), err)
	}

	tests := []struct {
		name     string
		token    string
		id       string
		wantCode int
	}{
		{"device cannot revoke another", selfTok, other.ID, http.StatusForbidden},
		{"unknown id", hostToken, "nope", http.StatusNotFound},
		{"host revokes", hostToken, other.ID, http.StatusNoContent},
		{"device revokes itself", selfTok, self.ID, http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := rig.do(http.MethodDelete, "/v1/devices/"+tt.id, tt.token, ""); rec.Code != tt.wantCode {
				t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
			}
		})
	}
	devs, _ := rig.devices.List()
	if len(devs) != 0 {
		t.Fatalf("left %+v", devs)
	}
}

// The app reads error.data to show what a terminate would lose.
func TestRPCPassesRefusalDataThrough(t *testing.T) {
	rig := newDeviceRig(t, nil)
	rig.srv.Socket = startEchoUnix(t, func(conn net.Conn, line []byte) {
		var req server.Request
		_ = json.Unmarshal(line, &req)
		out, _ := server.EncodeResponse(server.Response{ID: req.ID, Error: &server.Error{
			Code: server.CodeWorktreeDirty, Message: "dirty",
			Data: server.WorktreeDirtyData{ChangedFiles: 1, Paths: []string{"a.txt"}, UnpushedCommits: 2},
		}})
		_, _ = conn.Write(out)
	})
	_, tok := rig.device(t, false)
	rec := rig.do(http.MethodPost, "/v1/rpc", tok, `{"method":"session.terminate","params":{"session":"s1"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	want := `"data":{"changed_files":1,"paths":["a.txt"],"unpushed_commits":2}`
	if !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("body %s lacks %s", rec.Body.String(), want)
	}
}
