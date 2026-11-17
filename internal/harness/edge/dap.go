package edge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// KindDAP marks a relay that carries the Debug Adapter Protocol under a
// lease: one holder at a time. The edge reads each connection's first DAP
// message (the initialize request) to learn who it is. The first connection
// takes the lease; a connection that opens with the line
// "aiman-dap-lease <token>" names a lease token, and later connections with
// the same token join the holder's lease (up to MaxLeaseConns, for an
// adapter's child sessions, as js-debug opens through startDebugging). Any
// other client is answered with a DAP error naming the holder. The lease
// ends when its last connection closes or serve drops it (take over,
// release). The edge knows no debugger: dlv, js-debug or debugpy are all DAP
// servers behind the target address.
const KindDAP = "dap"

// MaxLeaseConns caps the connections one lease holds at once.
const MaxLeaseConns = 8

// LeasePrefix opens the optional lease line a client sends before DAP bytes.
const LeasePrefix = "aiman-dap-lease "

// MaxDAPMessage bounds one DAP message the edge or the console reads.
const MaxDAPMessage = 16 << 20

const (
	// dapHelloTimeout bounds the wait for a new client's first message.
	dapHelloTimeout = 10 * time.Second
	// dapFirstMessage bounds that first message.
	dapFirstMessage = 64 << 10
	// dapDialWindow is how long the relay keeps dialling the adapter, which
	// restarts after each client leaves.
	dapDialWindow = 20 * time.Second
	maxDAPHeader  = 8 << 10
)

// DAPHolder is the client that holds a DAP relay's lease: the first
// connection's identity and how many connections the lease holds now.
type DAPHolder struct {
	ClientID    string    `json:"client_id,omitempty"`
	ClientName  string    `json:"client_name,omitempty"`
	Remote      string    `json:"remote"`
	Since       time.Time `json:"since"`
	Connections int       `json:"connections"`
}

// dapLease is the live holder: its token (empty for a client that sent no
// lease line, which can hold one connection only) and its connections.
type dapLease struct {
	token string
	info  DAPHolder
	conns map[*dapConn]struct{}
}

// dapConn is one connection of a lease and the adapter connection behind it.
type dapConn struct {
	client   net.Conn
	upstream net.Conn
}

var leaseTokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// readLease reads the optional lease line. A connection without one has an
// empty token.
func readLease(br *bufio.Reader) (string, error) {
	head, err := br.Peek(len(LeasePrefix))
	if err != nil || string(head) != LeasePrefix {
		return "", nil //nolint:nilerr // too short for a lease line: plain DAP, ReadDAPMessage reports the error
	}
	line, err := br.ReadSlice('\n')
	if err != nil {
		return "", errors.New("dap: lease line too long or cut")
	}
	tok := strings.TrimSpace(strings.TrimPrefix(string(line), LeasePrefix))
	if !leaseTokenRe.MatchString(tok) {
		return "", errors.New("dap: bad lease token")
	}
	return tok, nil
}

// ReadDAPMessage reads one Content-Length framed message and returns its
// body. A body over limit, a missing length or a header over 8 KiB is an
// error.
func ReadDAPMessage(br *bufio.Reader, limit int) ([]byte, error) {
	length, read := -1, 0
	for {
		line, err := br.ReadString('\n')
		read += len(line)
		if err != nil {
			return nil, err
		}
		if read > maxDAPHeader {
			return nil, errors.New("dap: header too long")
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("dap: bad header line %q", line)
		}
		if strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < 0 {
				return nil, fmt.Errorf("dap: bad Content-Length %q", v)
			}
			length = n
		}
	}
	if length < 0 {
		return nil, errors.New("dap: no Content-Length")
	}
	if length > limit {
		return nil, fmt.Errorf("dap: message of %d bytes is over %d", length, limit)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(br, body); err != nil {
		return nil, err
	}
	return body, nil
}

// WriteDAPMessage writes v as one framed message.
func WriteDAPMessage(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return WriteDAPBody(w, body)
}

// WriteDAPBody frames body, which must be one JSON message.
func WriteDAPBody(w io.Writer, body []byte) error {
	var b bytes.Buffer
	b.Grow(len(body) + 32)
	fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n", len(body))
	b.Write(body)
	_, err := w.Write(b.Bytes())
	return err
}

type dapRequest struct {
	Seq       int    `json:"seq"`
	Type      string `json:"type"`
	Command   string `json:"command"`
	Arguments struct {
		ClientID   string `json:"clientID"`
		ClientName string `json:"clientName"`
	} `json:"arguments"`
}

// handleDAP admits client into the lease, or answers its first request with
// an error naming the holder.
func (r *relay) handleDAP(client net.Conn) {
	br := bufio.NewReader(client)
	_ = client.SetReadDeadline(time.Now().Add(dapHelloTimeout))
	token, err := readLease(br)
	var first []byte
	if err == nil {
		first, err = ReadDAPMessage(br, dapFirstMessage)
	}
	_ = client.SetReadDeadline(time.Time{})
	if err != nil {
		r.failed.Add(1)
		return
	}
	var req dapRequest
	if json.Unmarshal(first, &req) != nil || req.Type != "request" {
		r.failed.Add(1)
		return
	}
	dc := &dapConn{client: client}
	info := DAPHolder{
		ClientID: clip(req.Arguments.ClientID), ClientName: clip(req.Arguments.ClientName),
		Remote: client.RemoteAddr().String(), Since: time.Now().UTC(),
	}
	if r.target() == "" {
		r.failed.Add(1)
		_ = client.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_ = WriteDAPMessage(client, dapError(req, "nothing to debug yet: this run debugs its checks; start one with aiman harness check <name> --debug or the console's Debug check"))
		return
	}
	if cur, ok := r.claim(token, info, dc); !ok {
		r.refuse(client, req, cur)
		return
	}
	defer r.unclaim(dc)
	upstream, err := r.dialAdapter()
	if err != nil {
		r.failed.Add(1)
		r.setErr(err)
		_ = WriteDAPMessage(client, dapError(req, "the debugger did not answer: "+err.Error()))
		return
	}
	if !r.track(upstream, false) || !r.setUpstream(dc, upstream) {
		_ = upstream.Close()
		return
	}
	defer r.untrack(upstream)
	defer func() { _ = upstream.Close() }()
	if err := WriteDAPBody(upstream, first); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, br)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, upstream)
		done <- struct{}{}
	}()
	// Either side ending ends the connection: an adapter session serves one
	// client, and a client that left must free its place at once.
	<-done
	_ = client.Close()
	_ = upstream.Close()
	<-done
}

// dialAdapter keeps dialling the target for dapDialWindow, because the
// adapter restarts after each client, and a debug check's debugger listens
// only once the check container has started.
func (r *relay) dialAdapter() (net.Conn, error) {
	ctx, cancel := context.WithTimeout(r.ctx, dapDialWindow)
	defer cancel()
	var d net.Dialer
	for {
		target := r.target()
		if target == "" {
			return nil, errors.New("the debug target went away")
		}
		dctx, dcancel := context.WithTimeout(ctx, r.dialTimeout)
		c, err := d.DialContext(dctx, "tcp", target)
		dcancel()
		if err == nil {
			return c, nil
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// claim admits dc: a new lease when nobody holds the relay, else a place in
// the holder's lease when the token matches and the lease has room. It
// returns the holder when it refuses.
func (r *relay) claim(token string, info DAPHolder, dc *dapConn) (*DAPHolder, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lease == nil {
		r.lease = &dapLease{token: token, info: info, conns: map[*dapConn]struct{}{dc: {}}}
		return nil, true
	}
	l := r.lease
	if token != "" && token == l.token && len(l.conns) < MaxLeaseConns {
		l.conns[dc] = struct{}{}
		return nil, true
	}
	cur := l.info
	cur.Connections = len(l.conns)
	return &cur, false
}

func (r *relay) setUpstream(dc *dapConn, up net.Conn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lease == nil {
		return false
	}
	if _, ok := r.lease.conns[dc]; !ok {
		return false
	}
	dc.upstream = up
	return true
}

// unclaim takes dc out of its lease; the lease ends with its last
// connection.
func (r *relay) unclaim(dc *dapConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lease == nil {
		return
	}
	delete(r.lease.conns, dc)
	if len(r.lease.conns) == 0 {
		r.lease = nil
	}
}

// drop revokes the lease and ends every connection it holds.
func (r *relay) drop() *DAPHolder {
	return r.retarget(nil)
}

// retarget revokes the lease, ends every connection it holds and, when
// target is set, points the relay at *target: a debug check's debugger, or
// the app's again. An empty *target admits no client until it is set.
func (r *relay) retarget(target *string) *DAPHolder {
	r.mu.Lock()
	if target != nil {
		r.spec.Target = *target
	}
	l := r.lease
	r.lease = nil
	var conns []*dapConn
	if l != nil {
		for dc := range l.conns {
			conns = append(conns, dc)
		}
	}
	r.mu.Unlock()
	if l == nil {
		return nil
	}
	for _, dc := range conns {
		_ = dc.client.Close()
		if dc.upstream != nil {
			_ = dc.upstream.Close()
		}
	}
	info := l.info
	info.Connections = len(conns)
	return &info
}

func (r *relay) holderInfo() *DAPHolder {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lease == nil {
		return nil
	}
	info := r.lease.info
	info.Connections = len(r.lease.conns)
	return &info
}

func (r *relay) refuse(client net.Conn, req dapRequest, cur *DAPHolder) {
	r.refused.Add(1)
	who := cur.ClientName
	if who == "" {
		who = cur.ClientID
	}
	if who == "" {
		who = "another client"
	}
	msg := fmt.Sprintf("the debugger is held by %s since %s; take over in the harness console or with aiman harness debug take",
		who, cur.Since.Format(time.RFC3339))
	_ = client.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = WriteDAPMessage(client, dapError(req, msg))
}

func dapError(req dapRequest, msg string) map[string]any {
	return map[string]any{
		"seq": 0, "type": "response", "request_seq": req.Seq, "success": false, "command": req.Command,
		"message": msg, "body": map[string]any{"error": map[string]any{"id": 1, "format": msg, "showUser": true}},
	}
}

// clip keeps a client-sent label short and printable.
func clip(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	for len(s) > 80 {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}
