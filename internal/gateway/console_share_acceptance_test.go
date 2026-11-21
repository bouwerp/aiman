package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/console"
)

// Acceptance tests for P4 (docs/specs/harness-share.md, "P4 gateway console
// and forwards"). The gateway console is driven without tsnet: WhoIs,
// HostNodes, Listen and Dial are the seams.

const (
	shareID     = "k3v9AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	consoleHost = "aiman-gateway-h:8090"
	operator    = "operator@example.com"
	hostNode    = "nHOST"
)

type shareRPC struct {
	mu     sync.Mutex
	state  string // share_get state
	calls  []rpcCall
	status string // harness.status run state
	ports  string // harness.status ports JSON
}

type rpcCall struct {
	method string
	params map[string]any
}

func newShareRPC() *shareRPC {
	return &shareRPC{state: "live", status: "ready",
		ports: `[{"name":"web","service":"app","host_port":43117},{"name":"db","service":"db","host_port":43118},{"name":"dap","service":"app","host_port":43119}]`}
}

func (f *shareRPC) set(fn func(*shareRPC)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *shareRPC) Call(_ context.Context, method string, params map[string]any) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, rpcCall{method, params})
	switch method {
	case "harness.share_get":
		if params["id"] != shareID {
			return nil, errors.New("not_found: no such share")
		}
		return json.RawMessage(fmt.Sprintf(`{"type":"harness_share_state","share":%q,"session":"s1","run":"r1","state":%q}`, shareID, f.state)), nil
	case "harness.status":
		return json.RawMessage(fmt.Sprintf(`{"type":"harness_run","run":{"id":"r1","session":"s1","scenario":"refund","sha":"a1b2c3","source":"file","state":%q,"ports":%s,"services":["app","db"],"steps":[],"checks":[]}}`, f.status, f.ports)), nil
	case "harness.verdict":
		return json.RawMessage(`{"type":"harness_verdict","verdict":{"run":"r1","step":0,"result":"pass"}}`), nil
	case "gateway.announce":
		return json.RawMessage(`{"type":"gateway_announced"}`), nil
	}
	return json.RawMessage(`{}`), nil
}

func (f *shareRPC) called(method string) []rpcCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []rpcCall
	for _, c := range f.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

// peers answers WhoIs with whatever peer is current.
type peers struct {
	mu   sync.Mutex
	peer Peer
	err  error
}

func (p *peers) set(peer Peer, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.peer, p.err = peer, err
}

func (p *peers) whoIs(context.Context, string) (Peer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.peer, p.err
}

// tailnet hands out real loopback listeners for the forwards and records
// which public addresses were asked for.
type tailnet struct {
	mu    sync.Mutex
	asked []string
	lns   map[string]net.Listener
	dials []string
	// backend answers each dial with an echo.
}

func (n *tailnet) listen(network, addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.asked = append(n.asked, addr)
	if n.lns == nil {
		n.lns = map[string]net.Listener{}
	}
	n.lns[addr] = ln
	return ln, nil
}

func (n *tailnet) listener(addr string) net.Listener {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.lns[addr]
}

func (n *tailnet) askedFor() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.asked...)
}

func (n *tailnet) dial(_ context.Context, _, addr string) (net.Conn, error) {
	n.mu.Lock()
	n.dials = append(n.dials, addr)
	n.mu.Unlock()
	a, b := net.Pipe()
	go func() { _, _ = io.Copy(b, b) }()
	return a, nil
}

func (n *tailnet) dialed() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.dials...)
}

type fixture struct {
	shares *ConsoleShares
	rpc    *shareRPC
	peers  *peers
	net    *tailnet
	h      http.Handler
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{rpc: newShareRPC(), peers: &peers{}, net: &tailnet{}}
	f.peers.set(Peer{Login: operator, NodeID: "nLAPTOP"}, nil)
	cs, err := NewConsoleShares(ConsoleOptions{
		Logins:    []string{"Operator@Example.com"},
		Hosts:     []string{"aiman-gateway-h", "aiman-gateway-h.tail1.ts.net", "100.64.0.5"},
		Port:      8090,
		HTTPS:     true,
		WhoIs:     f.peers.whoIs,
		HostNodes: func(context.Context) ([]string, error) { return []string{hostNode}, nil },
		RPC:       f.rpc,
		Listen:    f.net.listen,
		Dial:      f.net.dial,
		Log:       io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	f.shares, f.h = cs, cs.Handler()
	return f
}

func (f *fixture) do(t *testing.T, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://"+consoleHost)
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Host = consoleHost
	r.RemoteAddr = "100.64.0.9:51000"
	if token != "" {
		r.Header.Set(console.TokenHeader, token)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

// visit opens the share URL and returns the visit token from the redirect.
func (f *fixture) visit(t *testing.T) string {
	t.Helper()
	w := f.do(t, "GET", "/s/"+shareID, "", "")
	if w.Code != http.StatusFound {
		t.Fatalf("GET /s/<id>: %d %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	tok, ok := strings.CutPrefix(loc, "/#token=")
	if !ok || len(tok) != 43 || strings.ContainsAny(tok, "+/=") {
		t.Fatalf("Location %q is not /#token=<43 base64url chars>", loc)
	}
	return tok
}

func TestConsoleShareNeedsLogins(t *testing.T) {
	_, err := NewConsoleShares(ConsoleOptions{
		Hosts: []string{"h"}, Port: 8090, RPC: newShareRPC(),
		WhoIs:     func(context.Context, string) (Peer, error) { return Peer{}, nil },
		HostNodes: func(context.Context) ([]string, error) { return nil, nil },
	})
	if err == nil {
		t.Fatal("NewConsoleShares with no Logins succeeded")
	}
}

func TestConsoleShareRefusesPeers(t *testing.T) {
	for _, tc := range []struct {
		name string
		peer Peer
		err  error
	}{
		{"login not listed", Peer{Login: "intruder@example.com", NodeID: "nX"}, nil},
		{"whois fails", Peer{}, errors.New("no peer")},
		{"no node id", Peer{Login: operator}, nil},
		{"host node", Peer{Login: operator, NodeID: hostNode}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.peers.set(tc.peer, tc.err)
			if w := f.do(t, "GET", "/s/"+shareID, "", ""); w.Code != http.StatusForbidden {
				t.Errorf("GET /s/<id>: %d, want 403", w.Code)
			}
			if w := f.do(t, "GET", "/", "", ""); w.Code != http.StatusForbidden {
				t.Errorf("GET /: %d, want 403", w.Code)
			}
			if n := len(f.rpc.called("harness.share_get")); n != 0 {
				t.Errorf("share_get called %d times for a refused peer", n)
			}
		})
	}
}

func TestConsoleShareHostNodesErrorFailsClosed(t *testing.T) {
	f := newFixture(t)
	cs, err := NewConsoleShares(ConsoleOptions{
		Logins: []string{operator}, Hosts: []string{"aiman-gateway-h"}, Port: 8090, HTTPS: true,
		WhoIs:     f.peers.whoIs,
		HostNodes: func(context.Context) ([]string, error) { return nil, errors.New("status failed") },
		RPC:       f.rpc, Listen: f.net.listen, Dial: f.net.dial, Log: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	f.h = cs.Handler()
	if w := f.do(t, "GET", "/s/"+shareID, "", ""); w.Code != http.StatusForbidden {
		t.Errorf("GET /s/<id> with HostNodes failing: %d, want 403", w.Code)
	}
}

func TestConsoleShareVisitAndState(t *testing.T) {
	f := newFixture(t)
	tok := f.visit(t)
	if w := f.do(t, "GET", "/api/state", tok, ""); w.Code != http.StatusOK {
		t.Fatalf("GET /api/state with visit token: %d %s", w.Code, w.Body.String())
	}
	if w := f.do(t, "GET", "/", "", ""); w.Code != http.StatusOK {
		t.Errorf("GET / for a listed login: %d", w.Code)
	}
	for _, bad := range []string{"", "nope", tok[:20]} {
		if w := f.do(t, "GET", "/api/state", bad, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("token %q: %d, want 401", bad, w.Code)
		}
	}
	// The token belongs to the login it was minted for.
	f.peers.set(Peer{Login: "other@example.com", NodeID: "nOTHER"}, nil)
	if w := f.do(t, "GET", "/api/state", tok, ""); w.Code == http.StatusOK {
		t.Errorf("another peer reused the visit token: %d", w.Code)
	}
}

func TestConsoleShareEndedOrUnknown(t *testing.T) {
	f := newFixture(t)
	f.rpc.set(func(r *shareRPC) { r.state = "ended" })
	if w := f.do(t, "GET", "/s/"+shareID, "", ""); w.Code != http.StatusNotFound {
		t.Errorf("ended share: %d, want 404", w.Code)
	}
	if w := f.do(t, "GET", "/s/unknownunknownunknownunknownunknownunknown0", "", ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown share: %d, want 404", w.Code)
	}
}

func TestConsoleShareMethodSet(t *testing.T) {
	f := newFixture(t)
	tok := f.visit(t)
	w := f.do(t, "POST", "/api/up", tok, `{"scenario":"refund"}`)
	if w.Code != http.StatusForbidden {
		t.Errorf("POST /api/up: %d, want 403", w.Code)
	}
	if n := len(f.rpc.called("harness.up")); n != 0 {
		t.Errorf("harness.up reached serve %d times", n)
	}
	w = f.do(t, "POST", "/api/down", tok, `{"run":"r1"}`)
	if w.Code != http.StatusForbidden {
		t.Errorf("POST /api/down: %d, want 403", w.Code)
	}
	if n := len(f.rpc.called("harness.down")); n != 0 {
		t.Errorf("harness.down reached serve %d times", n)
	}
}

func TestConsoleShareVerdictCarriesTester(t *testing.T) {
	f := newFixture(t)
	tok := f.visit(t)
	w := f.do(t, "POST", "/api/verdict", tok, `{"run":"r1","step":0,"result":"pass","note":"ok"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/verdict: %d %s", w.Code, w.Body.String())
	}
	calls := f.rpc.called("harness.verdict")
	if len(calls) != 1 {
		t.Fatalf("harness.verdict calls: %d", len(calls))
	}
	if got := calls[0].params["tester"]; got != operator {
		t.Errorf("tester = %v, want %s", got, operator)
	}
	if got := calls[0].params["run"]; got != "r1" {
		t.Errorf("run = %v, want r1", got)
	}
}

func TestConsoleShareEndedByEvent(t *testing.T) {
	f := newFixture(t)
	tok := f.visit(t)
	f.shares.ShareEvent(shareID, "ended")
	if w := f.do(t, "GET", "/api/state", tok, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("after the ended event: %d, want 401", w.Code)
	}
}

func TestConsoleShareEndedByPoll(t *testing.T) {
	f := newFixture(t)
	tok := f.visit(t)
	f.rpc.set(func(r *shareRPC) { r.state = "ended" })
	f.shares.PollOnce(context.Background())
	if w := f.do(t, "GET", "/api/state", tok, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("after a poll saw it ended: %d, want 401", w.Code)
	}
}

func TestConsoleShareAnnounce(t *testing.T) {
	rpc := newShareRPC()
	err := Announce(context.Background(), rpc, AnnounceParams{
		Base: "https://aiman-gateway-h.tail1.ts.net", ConsolePort: 8090, Scheme: "https", PID: 4242,
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := rpc.called("gateway.announce")
	if len(calls) != 1 {
		t.Fatalf("gateway.announce calls: %d", len(calls))
	}
	p := calls[0].params
	if p["base"] != "https://aiman-gateway-h.tail1.ts.net" || p["scheme"] != "https" ||
		fmt.Sprint(p["console_port"]) != "8090" || fmt.Sprint(p["pid"]) != "4242" {
		t.Errorf("announce params %v", p)
	}
}

// waitFor polls cond for up to 2s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func openForward(t *testing.T, f *fixture) net.Listener {
	t.Helper()
	f.visit(t)
	f.shares.PollOnce(context.Background())
	waitFor(t, "the app port forward", func() bool { return f.net.listener(":43117") != nil })
	return f.net.listener(":43117")
}

func TestConsoleForwardAppPortsOnly(t *testing.T) {
	f := newFixture(t)
	openForward(t, f)
	for _, a := range f.net.askedFor() {
		if a != ":43117" {
			t.Errorf("forward opened at %s; only the app port :43117 may be", a)
		}
	}
}

func TestConsoleForwardNeverGatewayPorts(t *testing.T) {
	f := newFixture(t)
	f.rpc.set(func(r *shareRPC) {
		r.ports = `[{"name":"web","service":"app","host_port":8090},{"name":"api","service":"app","host_port":8080},{"name":"ui","service":"app","host_port":43120}]`
	})
	f.visit(t)
	f.shares.PollOnce(context.Background())
	waitFor(t, "the ui forward", func() bool { return f.net.listener(":43120") != nil })
	for _, a := range f.net.askedFor() {
		if a == ":8080" || a == ":8090" {
			t.Errorf("forward opened at gateway port %s", a)
		}
	}
}

func TestConsoleForwardAdmitsVisitorOnly(t *testing.T) {
	f := newFixture(t)
	ln := openForward(t, f)

	// The visitor's connection reaches the edge port.
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("visitor's forward: %q %v", buf, err)
	}
	_ = c.Close()
	if d := f.net.dialed(); len(d) != 1 || d[0] != "127.0.0.1:43117" {
		t.Fatalf("dials %v, want [127.0.0.1:43117]", d)
	}

	// A listed login with no visit token is closed without a dial.
	f.peers.set(Peer{Login: "second@example.com", NodeID: "nSECOND"}, nil)
	c2, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = c2.SetDeadline(time.Now().Add(2 * time.Second))
	_, _ = c2.Write([]byte("ping"))
	if n, err := c2.Read(buf); err == nil {
		t.Errorf("non-visitor read %d bytes", n)
	}
	_ = c2.Close()
	if d := f.net.dialed(); len(d) != 1 {
		t.Errorf("non-visitor caused a dial: %v", d)
	}
}

func TestConsoleForwardClosesWhenShareEnds(t *testing.T) {
	f := newFixture(t)
	ln := openForward(t, f)
	addr := ln.Addr().String()
	f.shares.ShareEvent(shareID, "ended")
	waitFor(t, "the forward listener to close", func() bool {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			return true
		}
		_ = c.Close()
		return false
	})
}
