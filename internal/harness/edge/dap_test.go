package edge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDAPServer is a debug adapter on an inner stand-in address: for each
// connection it answers every request with a success response naming the
// connection's number, so a client can tell which upstream it reached.
type fakeDAPServer struct {
	addr  string
	conns atomic.Int64
	ln    net.Listener
}

func newFakeDAPServer(t *testing.T) *fakeDAPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeDAPServer{addr: ln.Addr().String(), ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n := s.conns.Add(1)
			go func() {
				defer func() { _ = c.Close() }()
				br := bufio.NewReader(c)
				for {
					body, err := ReadDAPMessage(br, MaxDAPMessage)
					if err != nil {
						return
					}
					var req struct {
						Seq     int    `json:"seq"`
						Command string `json:"command"`
					}
					_ = json.Unmarshal(body, &req)
					_ = WriteDAPMessage(c, map[string]any{
						"seq": 0, "type": "response", "request_seq": req.Seq, "success": true,
						"command": req.Command, "body": map[string]any{"conn": n},
					})
				}
			}()
		}
	}()
	return s
}

type dapClient struct {
	t  *testing.T
	c  net.Conn
	br *bufio.Reader
}

func dialDAP(t *testing.T, addr string) *dapClient {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return &dapClient{t: t, c: c, br: bufio.NewReader(c)}
}

// request sends one request and returns the decoded reply, or an error when
// the relay hung up.
func (d *dapClient) request(seq int, command string, args any) (map[string]any, error) {
	_ = d.c.SetDeadline(time.Now().Add(5 * time.Second))
	if err := WriteDAPMessage(d.c, map[string]any{"seq": seq, "type": "request", "command": command, "arguments": args}); err != nil {
		return nil, err
	}
	body, err := ReadDAPMessage(d.br, MaxDAPMessage)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	return out, json.Unmarshal(body, &out)
}

func initArgs(name string) map[string]any {
	return map[string]any{"clientID": "test", "clientName": name, "adapterID": "go"}
}

func TestDAPExclusiveClient(t *testing.T) {
	e := newTestEdge(t)
	srv := newFakeDAPServer(t)
	st, err := e.Add(RelaySpec{Name: "dap", Target: srv.addr, Kind: KindDAP})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if st.Kind != KindDAP || st.Holder != nil {
		t.Fatalf("status %+v", st)
	}

	first := dialDAP(t, st.Listen)
	resp, err := first.request(1, "initialize", initArgs("aiman console"))
	if err != nil || resp["success"] != true {
		t.Fatalf("first initialize: %v %v", resp, err)
	}
	holder := waitHolder(t, e, "dap", "aiman console")
	if holder.ClientID != "test" || holder.Since.IsZero() {
		t.Fatalf("holder %+v", holder)
	}

	// A second client is refused with a DAP error naming the holder, and the
	// first keeps working on the same upstream.
	second := dialDAP(t, st.Listen)
	resp, err = second.request(1, "initialize", initArgs("vscode"))
	if err != nil {
		t.Fatalf("second client got no answer: %v", err)
	}
	msg, _ := resp["message"].(string)
	if resp["success"] != false || resp["command"] != "initialize" || resp["request_seq"] != float64(1) ||
		!strings.Contains(msg, "aiman console") || !strings.Contains(msg, "take over") {
		t.Fatalf("second initialize = %v", resp)
	}
	if _, err := second.request(2, "launch", nil); err == nil {
		t.Fatal("refused client stayed connected")
	}
	if srv.conns.Load() != 1 {
		t.Fatalf("upstream saw %d connections, want 1 (the refused client must not reach it)", srv.conns.Load())
	}
	resp, err = first.request(2, "threads", nil)
	if err != nil || resp["success"] != true {
		t.Fatalf("holder after refusal: %v %v", resp, err)
	}
	if got := statusOf(t, e, "dap"); got.Refused != 1 {
		t.Fatalf("refused = %d, want 1", got.Refused)
	}

	// Take over: the edge closes the holder, and the next client holds.
	cl := Client{Socket: startControl(t, e), Token: testToken}
	dropped, err := cl.DAPDrop(context.Background(), "dap")
	if err != nil || dropped == nil || dropped.ClientName != "aiman console" {
		t.Fatalf("DAPDrop = %+v, %v", dropped, err)
	}
	if _, err := first.request(3, "threads", nil); err == nil {
		t.Fatal("dropped holder stayed connected")
	}
	waitNoHolder(t, e, "dap")
	third := dialDAP(t, st.Listen)
	resp, err = third.request(1, "initialize", initArgs("vscode"))
	if err != nil || resp["success"] != true {
		t.Fatalf("after take over: %v %v", resp, err)
	}
	if body, _ := resp["body"].(map[string]any); body["conn"] != float64(2) {
		t.Fatalf("new holder reached upstream connection %v, want a fresh one", body["conn"])
	}
	waitHolder(t, e, "dap", "vscode")

	// Release: the holder leaving on its own frees the debugger too.
	_ = third.c.Close()
	waitNoHolder(t, e, "dap")
	if _, err := cl.DAPDrop(context.Background(), "dap"); err != nil {
		t.Fatalf("drop with no holder: %v", err)
	}
	if _, err := cl.DAPDrop(context.Background(), "web"); !hasCode(err, CodeNotFound) {
		t.Fatalf("drop on a missing relay = %v", err)
	}
}

func TestDAPRelayRefusesNonDAP(t *testing.T) {
	e := newTestEdge(t)
	srv := newFakeDAPServer(t)
	st, err := e.Add(RelaySpec{Name: "dap", Target: srv.addr, Kind: KindDAP})
	if err != nil {
		t.Fatal(err)
	}
	c := dialDAP(t, st.Listen)
	_ = c.c.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(c.c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
	if _, err := io.ReadAll(c.c); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("read: %v", err)
	}
	if srv.conns.Load() != 0 || statusOf(t, e, "dap").Holder != nil {
		t.Fatal("a non-DAP client reached the debugger or holds it")
	}
}

func TestDAPRelayWaitsForAdapterRestart(t *testing.T) {
	e := newTestEdge(t)
	// Reserve an address, close it, and start the adapter there a moment
	// after the client connects, as dlv dap does after the last client left.
	ln, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	st, err := e.Add(RelaySpec{Name: "dap", Target: addr, Kind: KindDAP})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(700 * time.Millisecond)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return
		}
		t.Cleanup(func() { _ = ln.Close() })
		c, err := ln.Accept()
		if err != nil {
			return
		}
		br := bufio.NewReader(c)
		if _, err := ReadDAPMessage(br, MaxDAPMessage); err == nil {
			_ = WriteDAPMessage(c, map[string]any{"type": "response", "request_seq": 1, "success": true, "command": "initialize"})
		}
	}()
	c := dialDAP(t, st.Listen)
	resp, err := c.request(1, "initialize", initArgs("late"))
	if err != nil || resp["success"] != true {
		t.Fatalf("initialize across a restart: %v %v", resp, err)
	}
}

func TestReadDAPMessage(t *testing.T) {
	ok := "Content-Length: 2\r\n\r\n{}"
	if b, err := ReadDAPMessage(bufio.NewReader(strings.NewReader(ok)), 10); err != nil || string(b) != "{}" {
		t.Fatalf("ok = %q, %v", b, err)
	}
	for _, bad := range []string{
		"Content-Length: 20\r\n\r\n{}",
		"Content-Length: 99\r\n\r\n" + strings.Repeat("x", 99),
		"Content-Type: x\r\n\r\n{}",
		"Content-Length: -1\r\n\r\n",
		strings.Repeat("H", 9000),
	} {
		if _, err := ReadDAPMessage(bufio.NewReader(strings.NewReader(bad)), 10); err == nil {
			t.Fatalf("accepted %q", bad[:min(len(bad), 40)])
		}
	}
}

func statusOf(t *testing.T, e *Edge, name string) RelayStatus {
	t.Helper()
	for _, r := range e.List() {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no relay %s", name)
	return RelayStatus{}
}

func waitHolder(t *testing.T, e *Edge, relay, name string) DAPHolder {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if h := statusOf(t, e, relay).Holder; h != nil && h.ClientName == name {
			return *h
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("holder never became %q: %+v", name, statusOf(t, e, relay).Holder)
	return DAPHolder{}
}

func waitNoHolder(t *testing.T, e *Edge, relay string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if statusOf(t, e, relay).Holder == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(fmt.Sprintf("holder stayed: %+v", statusOf(t, e, relay).Holder))
}

// dialLease opens a connection that names a lease token first.
func dialLease(t *testing.T, addr, token string) *dapClient {
	t.Helper()
	d := dialDAP(t, addr)
	if _, err := io.WriteString(d.c, LeasePrefix+token+"\n"); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDAPExclusiveClientLease(t *testing.T) {
	e := newTestEdge(t)
	srv := newFakeDAPServer(t)
	st, err := e.Add(RelaySpec{Name: "dap", Target: srv.addr, Kind: KindDAP})
	if err != nil {
		t.Fatal(err)
	}
	const token = "lease-token-0123456789"
	var held []*dapClient
	for i := range MaxLeaseConns {
		c := dialLease(t, st.Listen, token)
		if resp, err := c.request(1, "initialize", initArgs("aiman console")); err != nil || resp["success"] != true {
			t.Fatalf("lease connection %d: %v %v", i+1, resp, err)
		}
		held = append(held, c)
	}
	if h := statusOf(t, e, "dap").Holder; h == nil || h.Connections != MaxLeaseConns || h.ClientName != "aiman console" {
		t.Fatalf("holder %+v", h)
	}
	for name, c := range map[string]*dapClient{
		"ninth with the token": dialLease(t, st.Listen, token),
		"another token":        dialLease(t, st.Listen, "other-token-0123456789"),
		"no lease line":        dialDAP(t, st.Listen),
	} {
		resp, err := c.request(1, "initialize", initArgs("intruder"))
		if err != nil || resp["success"] != false {
			t.Fatalf("%s: %v %v", name, resp, err)
		}
	}
	// A child session closing leaves the lease to the rest.
	_ = held[1].c.Close()
	deadline := time.Now().Add(3 * time.Second)
	for statusOf(t, e, "dap").Holder.Connections != MaxLeaseConns-1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if resp, err := held[0].request(2, "threads", nil); err != nil || resp["success"] != true {
		t.Fatalf("lease after a child left: %v %v", resp, err)
	}
	// Take over revokes the lease and closes every connection.
	dropped, err := e.DropDAP("dap")
	if err != nil || dropped == nil || dropped.Connections != MaxLeaseConns-1 {
		t.Fatalf("drop = %+v, %v", dropped, err)
	}
	for i, c := range held {
		if i == 1 {
			continue
		}
		if _, err := c.request(3, "threads", nil); err == nil {
			t.Fatalf("connection %d survived the take over", i)
		}
	}
	// A new client takes the free relay as a fresh lease, and the revoked
	// token cannot join it.
	c := dialLease(t, st.Listen, "fresh-token-0123456789")
	if resp, err := c.request(1, "initialize", initArgs("vscode")); err != nil || resp["success"] != true {
		t.Fatalf("after take over: %v %v", resp, err)
	}
	late := dialLease(t, st.Listen, token)
	if resp, _ := late.request(1, "initialize", nil); resp["success"] == true {
		t.Fatal("the revoked token joined the new lease")
	}
}

func TestDAPLeaseLineRefusesBadToken(t *testing.T) {
	e := newTestEdge(t)
	srv := newFakeDAPServer(t)
	st, err := e.Add(RelaySpec{Name: "dap", Target: srv.addr, Kind: KindDAP})
	if err != nil {
		t.Fatal(err)
	}
	c := dialLease(t, st.Listen, "short")
	if _, err := c.request(1, "initialize", nil); err == nil {
		t.Fatal("a bad lease token was admitted")
	}
	if statusOf(t, e, "dap").Holder != nil || srv.conns.Load() != 0 {
		t.Fatal("a bad lease token reached the debugger")
	}
}
