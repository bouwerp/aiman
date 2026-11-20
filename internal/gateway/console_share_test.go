package gateway

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/console"
)

func dapRequest(f *fixture, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", console.DAPPath, nil)
	r.Host = consoleHost
	r.RemoteAddr = "100.64.0.9:51000"
	r.Header.Set("Origin", "https://"+consoleHost)
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Sec-WebSocket-Version", "13")
	r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	r.Header.Set("Sec-WebSocket-Protocol", console.DAPSubprotocol+", aiman-token."+token)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

// The debugger bridge takes the visit token from the subprotocol offer and
// reaches the share's console with the console's own token: the run is not a
// debug run, so the console answers 409 rather than 401.
func TestConsoleShareDAPSubprotocolToken(t *testing.T) {
	f := newFixture(t)
	tok := f.visit(t)
	if w := dapRequest(f, tok); w.Code != http.StatusConflict {
		t.Errorf("visit token in the subprotocol: %d %s, want 409 no_debugger", w.Code, w.Body.String())
	}
	if w := dapRequest(f, "nope"); w.Code != http.StatusUnauthorized {
		t.Errorf("unknown token in the subprotocol: %d, want 401", w.Code)
	}
}

func TestConsoleForwardClosesWhenRunNotReady(t *testing.T) {
	f := newFixture(t)
	ln := openForward(t, f)
	addr := ln.Addr().String()
	f.rpc.set(func(r *shareRPC) { r.status = "stopped" })
	f.shares.PollOnce(context.Background())
	waitFor(t, "the forward listener to close", func() bool {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			return true
		}
		_ = c.Close()
		return false
	})
}
