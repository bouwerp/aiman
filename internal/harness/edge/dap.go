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
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// KindDAP marks a relay that carries the Debug Adapter Protocol to one client
// at a time. The edge reads the client's first message (the initialize
// request) to learn who it is, holds the debugger for that client until it
// leaves or serve drops it, and answers any other client with a DAP error
// naming the holder. It knows no debugger: dlv, js-debug or debugpy are all
// DAP servers behind the target address.
const KindDAP = "dap"

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

// DAPHolder is the client that holds a DAP relay.
type DAPHolder struct {
	ClientID   string    `json:"client_id,omitempty"`
	ClientName string    `json:"client_name,omitempty"`
	Remote     string    `json:"remote"`
	Since      time.Time `json:"since"`
}

// dapHold is the live holder and both of its connections.
type dapHold struct {
	info     DAPHolder
	client   net.Conn
	upstream net.Conn
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

// handleDAP admits client when nobody holds the relay, else answers its
// first request with an error naming the holder.
func (r *relay) handleDAP(client net.Conn) {
	br := bufio.NewReader(client)
	_ = client.SetReadDeadline(time.Now().Add(dapHelloTimeout))
	first, err := ReadDAPMessage(br, dapFirstMessage)
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
	hold := &dapHold{
		client: client,
		info: DAPHolder{
			ClientID: clip(req.Arguments.ClientID), ClientName: clip(req.Arguments.ClientName),
			Remote: client.RemoteAddr().String(), Since: time.Now().UTC(),
		},
	}
	if cur, ok := r.claim(hold); !ok {
		r.refuse(client, req, cur)
		return
	}
	defer r.unclaim(hold)
	upstream, err := r.dialAdapter()
	if err != nil {
		r.failed.Add(1)
		r.setErr(err)
		_ = WriteDAPMessage(client, dapError(req, "the debugger did not answer: "+err.Error()))
		return
	}
	if !r.track(upstream, false) || !r.setUpstream(hold, upstream) {
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
	// Either side ending ends the session: the adapter serves one client,
	// and a client that left must free the debugger at once.
	<-done
	_ = client.Close()
	_ = upstream.Close()
	<-done
}

// dialAdapter keeps dialling the target for dapDialWindow, because the
// adapter restarts after each client.
func (r *relay) dialAdapter() (net.Conn, error) {
	ctx, cancel := context.WithTimeout(r.ctx, dapDialWindow)
	defer cancel()
	var d net.Dialer
	for {
		dctx, dcancel := context.WithTimeout(ctx, r.dialTimeout)
		c, err := d.DialContext(dctx, "tcp", r.spec.Target)
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

func (r *relay) claim(h *dapHold) (*DAPHolder, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.holder != nil {
		cur := r.holder.info
		return &cur, false
	}
	r.holder = h
	return nil, true
}

func (r *relay) setUpstream(h *dapHold, up net.Conn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.holder != h {
		return false
	}
	h.upstream = up
	return true
}

func (r *relay) unclaim(h *dapHold) {
	r.mu.Lock()
	if r.holder == h {
		r.holder = nil
	}
	r.mu.Unlock()
}

// drop ends the holder's connections and frees the relay at once.
func (r *relay) drop() *DAPHolder {
	r.mu.Lock()
	h := r.holder
	r.holder = nil
	r.mu.Unlock()
	if h == nil {
		return nil
	}
	_ = h.client.Close()
	if h.upstream != nil {
		_ = h.upstream.Close()
	}
	info := h.info
	return &info
}

func (r *relay) holderInfo() *DAPHolder {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.holder == nil {
		return nil
	}
	info := r.holder.info
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
