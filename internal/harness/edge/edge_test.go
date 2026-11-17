package edge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testToken = "0123456789abcdef0123456789abcdef"

// shortTempDir is t.TempDir() with a short path, so a Unix socket under it
// stays below the 108-byte sun_path limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "edg")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

var loopback = netip.MustParseAddr("127.0.0.1")

func localLoopback() ([]netip.Addr, error) { return []netip.Addr{loopback}, nil }

func newTestEdge(t *testing.T) *Edge {
	t.Helper()
	e, err := New(Config{Outer: loopback, Token: []byte(testToken), LocalAddrs: localLoopback})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

// echoServer accepts connections and writes back what it reads until EOF. It
// listens on 127.0.0.2, which stands in for an inner address: the relay
// refuses its own outer address as a target.
func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String()
}

func roundTrip(t *testing.T, addr, msg string) string {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial relay %s: %v", addr, err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := c.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatalf("close write: %v", err)
	}
	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(got)
}

func TestRelayForwardsToTarget(t *testing.T) {
	e := newTestEdge(t)
	target := echoServer(t)
	st, err := e.Add(RelaySpec{Name: "web", Port: 0, Target: target})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if st.Port == 0 {
		t.Fatalf("status port not filled: %+v", st)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(st.Port))
	if got := roundTrip(t, addr, "hello edge"); got != "hello edge" {
		t.Fatalf("echo through relay = %q", got)
	}
	// A second connection on the same relay works too, and the counters see both.
	if got := roundTrip(t, addr, "again"); got != "again" {
		t.Fatalf("second echo = %q", got)
	}
	waitFor(t, func() bool { l := e.List(); return len(l) == 1 && l[0].Total == 2 && l[0].Active == 0 })
}

func TestRelayRemoveClosesListener(t *testing.T) {
	e := newTestEdge(t)
	st, err := e.Add(RelaySpec{Name: "web", Target: echoServer(t)})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := e.Remove("web"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(st.Port)), time.Second); err == nil {
		_ = c.Close()
		t.Fatal("relay port still accepts after Remove")
	}
	if err := e.Remove("web"); !hasCode(err, CodeNotFound) {
		t.Fatalf("second Remove = %v, want %s", err, CodeNotFound)
	}
	if len(e.List()) != 0 {
		t.Fatalf("List after Remove = %+v", e.List())
	}
}

func TestRelayRejectsBadSpecs(t *testing.T) {
	e := newTestEdge(t)
	target := echoServer(t)
	if _, err := e.Add(RelaySpec{Name: "web", Target: target}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	cases := []struct {
		name string
		spec RelaySpec
		code string
	}{
		{"duplicate name", RelaySpec{Name: "web", Target: target}, CodeExists},
		{"empty name", RelaySpec{Name: "", Target: target}, CodeInvalidParams},
		{"bad name chars", RelaySpec{Name: "we b/../x", Target: target}, CodeInvalidParams},
		{"long name", RelaySpec{Name: strings.Repeat("a", 64), Target: target}, CodeInvalidParams},
		{"negative port", RelaySpec{Name: "a", Port: -1, Target: target}, CodeInvalidParams},
		{"port too high", RelaySpec{Name: "a", Port: 70000, Target: target}, CodeInvalidParams},
		{"no target", RelaySpec{Name: "a"}, CodeInvalidParams},
		{"target without port", RelaySpec{Name: "a", Target: "db"}, CodeInvalidParams},
		{"target port zero", RelaySpec{Name: "a", Target: "db:0"}, CodeInvalidParams},
		{"target is the outer address", RelaySpec{Name: "a", Target: "127.0.0.1:9"}, CodeInvalidParams},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := e.Add(tc.spec); !hasCode(err, tc.code) {
				t.Fatalf("Add(%+v) = %v, want %s", tc.spec, err, tc.code)
			}
		})
	}
}

func TestRelayDialFailureCounted(t *testing.T) {
	e := newTestEdge(t)
	// A port nothing listens on: take one and close it.
	ln, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skipf("127.0.0.2 not usable: %v", err)
	}
	dead := ln.Addr().String()
	_ = ln.Close()
	st, err := e.Add(RelaySpec{Name: "db", Target: dead})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(st.Port)), time.Second)
	if err != nil {
		t.Fatalf("dial relay: %v", err)
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadAll(c); err != nil && !errors.Is(err, io.EOF) {
		var ne net.Error
		if !errors.As(err, &ne) || ne.Timeout() {
			t.Fatalf("relay did not close the client: %v", err)
		}
	}
	_ = c.Close()
	waitFor(t, func() bool { l := e.List(); return len(l) == 1 && l[0].Failed == 1 && l[0].LastError != "" })
}

func TestRelayBindsOuterOnly(t *testing.T) {
	e := newTestEdge(t)
	st, err := e.Add(RelaySpec{Name: "web", Target: echoServer(t)})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if st.Listen != net.JoinHostPort("127.0.0.1", strconv.Itoa(st.Port)) {
		t.Fatalf("relay listens on %q, want the outer address", st.Listen)
	}
	// 127.0.0.2 stands in for the inner address: the host routes it to lo, so a
	// wildcard bind would answer on it. The outer-only bind must not.
	inner := net.JoinHostPort("127.0.0.2", strconv.Itoa(st.Port))
	if c, err := net.DialTimeout("tcp", inner, time.Second); err == nil {
		_ = c.Close()
		t.Fatalf("relay answered on %s; it must bind the outer address only", inner)
	}
}

func TestRelayBindsOuterOnlyRefusesBadOuter(t *testing.T) {
	inner := []netip.Prefix{netip.MustParsePrefix("172.30.0.0/24")}
	local := func() ([]netip.Addr, error) {
		return []netip.Addr{loopback, netip.MustParseAddr("172.30.0.5"), netip.MustParseAddr("172.31.0.5")}, nil
	}
	cases := []struct {
		name  string
		outer netip.Addr
	}{
		{"unset", netip.Addr{}},
		{"ipv4 wildcard", netip.MustParseAddr("0.0.0.0")},
		{"ipv6 wildcard", netip.MustParseAddr("::")},
		{"multicast", netip.MustParseAddr("224.0.0.1")},
		{"inner address", netip.MustParseAddr("172.30.0.5")},
		{"not a local address", netip.MustParseAddr("10.9.9.9")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Config{Outer: tc.outer, Inner: inner, Token: []byte(testToken), LocalAddrs: local})
			if err == nil {
				t.Fatalf("New accepted outer %v", tc.outer)
			}
		})
	}
	e, err := New(Config{Outer: netip.MustParseAddr("172.31.0.5"), Inner: inner, Token: []byte(testToken), LocalAddrs: local})
	if err != nil {
		t.Fatalf("New refused a valid outer address: %v", err)
	}
	_ = e.Close()
}

func TestResolveOuterFromPrefix(t *testing.T) {
	local := func() ([]netip.Addr, error) {
		return []netip.Addr{loopback, netip.MustParseAddr("172.30.0.5"), netip.MustParseAddr("172.31.0.7")}, nil
	}
	got, err := ResolveOuter("172.31.0.0/16", local)
	if err != nil || got != netip.MustParseAddr("172.31.0.7") {
		t.Fatalf("ResolveOuter(prefix) = %v, %v", got, err)
	}
	got, err = ResolveOuter("172.30.0.5", local)
	if err != nil || got != netip.MustParseAddr("172.30.0.5") {
		t.Fatalf("ResolveOuter(addr) = %v, %v", got, err)
	}
	if _, err := ResolveOuter("10.0.0.0/8", local); err == nil {
		t.Fatal("ResolveOuter matched a prefix with no local address")
	}
	if _, err := ResolveOuter("nonsense", local); err == nil {
		t.Fatal("ResolveOuter accepted garbage")
	}
}

func TestNewRefusesShortToken(t *testing.T) {
	if _, err := New(Config{Outer: loopback, Token: []byte("short"), LocalAddrs: localLoopback}); err == nil {
		t.Fatal("New accepted a short token")
	}
}

func startControl(t *testing.T, e *Edge) string {
	t.Helper()
	sock := filepath.Join(shortTempDir(t), SocketName)
	ln, err := ListenControl(sock)
	if err != nil {
		t.Fatalf("ListenControl: %v", err)
	}
	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("socket mode %v is open to group or others", fi.Mode().Perm())
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.ServeControl(ln)
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
	})
	return sock
}

// rawControl sends one raw line and returns the decoded reply.
func rawControl(t *testing.T, sock, line string) Response {
	t.Helper()
	c, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		t.Fatalf("dial control: %v", err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, line+"\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := bufio.NewReader(c)
	reply, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	var resp Response
	if err := json.Unmarshal(reply, &resp); err != nil {
		t.Fatalf("decode %q: %v", reply, err)
	}
	return resp
}

func TestControlNeedsToken(t *testing.T) {
	e := newTestEdge(t)
	sock := startControl(t, e)
	target := echoServer(t)
	add := func(token string) string {
		req := map[string]any{"op": OpAdd, "relay": RelaySpec{Name: "web", Target: target}}
		if token != "" {
			req["token"] = token
		}
		b, _ := json.Marshal(req)
		return string(b)
	}
	cases := []struct{ name, line string }{
		{"no token", add("")},
		{"wrong token same length", add(strings.Repeat("x", len(testToken)))},
		{"token prefix", add(testToken[:10])},
		{"token with suffix", add(testToken + "x")},
		{"list without token", `{"op":"list"}`},
		{"health without token", `{"op":"health"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := rawControl(t, sock, tc.line)
			if resp.OK || resp.Error == nil || resp.Error.Code != CodeUnauthorized {
				t.Fatalf("reply = %+v, want %s", resp, CodeUnauthorized)
			}
			if resp.Relays != nil || resp.Health != nil {
				t.Fatalf("unauthorized reply carries data: %+v", resp)
			}
		})
	}
	if len(e.List()) != 0 {
		t.Fatalf("an unauthorized add created a relay: %+v", e.List())
	}
	resp := rawControl(t, sock, add(testToken))
	if !resp.OK || resp.Relay == nil || resp.Relay.Name != "web" {
		t.Fatalf("authorized add = %+v", resp)
	}
}

func TestControlUnauthorizedClosesConnection(t *testing.T) {
	e := newTestEdge(t)
	sock := startControl(t, e)
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(c, `{"op":"list","token":"nope"}`+"\n")
	r := bufio.NewReader(c)
	if _, err := r.ReadBytes('\n'); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	// The edge hangs up after a bad token, so a second guess needs a new dial.
	_, _ = io.WriteString(c, `{"op":"list","token":"`+testToken+`"}`+"\n")
	if line, err := r.ReadBytes('\n'); err == nil {
		t.Fatalf("connection stayed open after a bad token: %q", line)
	}
}

func TestControlClientRoundTrip(t *testing.T) {
	e := newTestEdge(t)
	cl := Client{Socket: startControl(t, e), Token: testToken}
	ctx := context.Background()
	st, err := cl.Add(ctx, RelaySpec{Name: "web", Target: echoServer(t)})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := roundTrip(t, st.Listen, "via control"); got != "via control" {
		t.Fatalf("echo = %q", got)
	}
	list, err := cl.List(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "web" {
		t.Fatalf("List = %+v, %v", list, err)
	}
	h, err := cl.Health(ctx)
	if err != nil || h.Outer != "127.0.0.1" || len(h.Relays) != 1 || h.Relays[0].Total != 1 {
		t.Fatalf("Health = %+v, %v", h, err)
	}
	if _, err := cl.Add(ctx, RelaySpec{Name: "web", Target: "db:5432"}); !hasCode(err, CodeExists) {
		t.Fatalf("duplicate Add = %v, want %s", err, CodeExists)
	}
	if err := cl.Remove(ctx, "web"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := cl.Remove(ctx, "web"); !hasCode(err, CodeNotFound) {
		t.Fatalf("Remove missing = %v, want %s", err, CodeNotFound)
	}
	bad := Client{Socket: cl.Socket, Token: "wrong-token-wrong-token-wrong-tok"}
	if _, err := bad.List(ctx); !hasCode(err, CodeUnauthorized) {
		t.Fatalf("bad token List = %v, want %s", err, CodeUnauthorized)
	}
}

func TestControlRejectsUnknownOpAndGarbage(t *testing.T) {
	e := newTestEdge(t)
	sock := startControl(t, e)
	if resp := rawControl(t, sock, `{"op":"reboot","token":"`+testToken+`"}`); resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("unknown op = %+v", resp)
	}
	if resp := rawControl(t, sock, `not json`); resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("garbage = %+v", resp)
	}
	if resp := rawControl(t, sock, `{"op":"list","token":"`+testToken+`","extra":1}`); resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("unknown field = %+v", resp)
	}
}

func TestListenControlReplacesStaleSocket(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), SocketName)
	ln, err := ListenControl(sock)
	if err != nil {
		t.Fatalf("first listen: %v", err)
	}
	_ = ln.Close()
	// net.UnixListener.Close unlinks; leave a stale file as a crash would.
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatalf("stale file: %v", err)
	}
	ln, err = ListenControl(sock)
	if err != nil {
		t.Fatalf("listen over stale socket: %v", err)
	}
	_ = ln.Close()
}

func TestReadTokenFile(t *testing.T) {
	dir := shortTempDir(t)
	p := filepath.Join(dir, TokenName)
	if err := os.WriteFile(p, []byte(testToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tok, err := ReadTokenFile(p)
	if err != nil || string(tok) != testToken {
		t.Fatalf("ReadTokenFile = %q, %v", tok, err)
	}
	if err := os.WriteFile(p, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTokenFile(p); err == nil {
		t.Fatal("ReadTokenFile accepted an empty token")
	}
}

func hasCode(err error, code string) bool {
	var ce *Error
	return errors.As(err, &ce) && ce.Code == code
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in 5s")
}
