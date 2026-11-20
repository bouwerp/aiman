package gateway

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/console"
	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/usecase"
)

// The gateway hosts shared harness consoles on the tailnet
// (docs/specs/harness-share-design.md, "Gateway console"). A session shares
// its ready run with `aiman harness share`; serve prints
// <base>:<console port>/s/<id>. A browser on the tailnet opens that URL, the
// gateway checks the peer, asks serve whether the share is live and answers
// with a visit token in the fragment. Every API call then carries the visit
// token and goes to that share's console.Server, which is pinned to the
// share's run and may call only the tester's methods.

const (
	// VisitTokenTTL is how long a visit token lives without use.
	VisitTokenTTL = 12 * time.Hour
	// SharePollEvery is how often Run asks serve about each live share.
	SharePollEvery = 10 * time.Second
	// consoleReadyWait bounds the wait for a new console's first poll.
	consoleReadyWait = 5 * time.Second
	// shareIDLen is the length of a share id: 32 bytes, base64url.
	shareIDLen = 43
)

// TesterMethods are the serve methods a shared console may call: what a
// tester needs on a run the agent owns. harness.up and harness.down are left
// out on purpose: the agent starts and stops the run.
var TesterMethods = []string{
	"harness.status", "harness.list", "harness.logs", "harness.capture",
	"harness.check", "harness.verdict", "harness.debug", "harness.files",
	"harness.file", "harness.db", "harness.evm_txs", "harness.evm_trace",
	"harness.aws", "harness.aws_calls", "harness.aws_write",
}

// Peer is who a tailnet connection comes from.
type Peer struct {
	Login string
	// NodeID is the tailnet node's StableID.
	NodeID string
}

// PeerWhoIs resolves a tailnet connection's peer.
type PeerWhoIs func(ctx context.Context, remoteAddr string) (Peer, error)

// ConsoleOptions configure the gateway's shared consoles.
type ConsoleOptions struct {
	// Logins may open a share (gateway.console_logins), compared ignoring case.
	Logins []string
	// Hosts are the names the console answers on, without port; the first
	// names the app links.
	Hosts []string
	// Port is the console's public port.
	Port int
	// HTTPS says the console is served over TLS.
	HTTPS bool
	WhoIs PeerWhoIs
	// HostNodes are the StableIDs of the host's own nodes, which are refused.
	HostNodes func(ctx context.Context) ([]string, error)
	// RPC calls serve as the operator.
	RPC console.RPC
	// Listen opens an app-port forward on the tailnet.
	Listen func(network, addr string) (net.Listener, error)
	// Dial reaches the edge on the host; nil is a net.Dialer.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Log receives refusals and failures, never a token.
	Log io.Writer
}

// ConsoleShares hosts the shared consoles.
type ConsoleShares struct {
	o      ConsoleOptions
	logins map[string]bool
	// static serves the page's files; it has no run and calls nothing.
	static http.Handler

	mu     sync.Mutex
	closed bool
	shares map[string]*share
	visits map[string]*visit
	now    func() time.Time
}

// visit is one visit token: a browser of login on node holding share.
type visit struct {
	share   *share
	login   string
	node    string
	expires time.Time
}

// share is one live share the gateway serves.
type share struct {
	id  string
	run string

	mu      sync.Mutex
	ended   bool
	console *console.Server
	// token is the console's own token, which the gateway sends in place of
	// the visit token.
	token string
	stop  context.CancelFunc
	// conns are hijacked debugger bridges and forwarded connections.
	conns map[net.Conn]struct{}
	fwds  map[int]*appForward
}

// NewConsoleShares checks the options and makes the gateway console.
func NewConsoleShares(o ConsoleOptions) (*ConsoleShares, error) {
	switch {
	case len(o.Logins) == 0:
		return nil, errors.New("gateway console: no console logins")
	case len(o.Hosts) == 0:
		return nil, errors.New("gateway console: no hosts")
	case o.Port <= 0 || o.Port > 65535:
		return nil, fmt.Errorf("gateway console: bad port %d", o.Port)
	case o.WhoIs == nil || o.HostNodes == nil:
		return nil, errors.New("gateway console: WhoIs and HostNodes are required")
	case o.RPC == nil:
		return nil, errors.New("gateway console: RPC is required")
	}
	if o.Dial == nil {
		var d net.Dialer
		o.Dial = d.DialContext
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
	logins := map[string]bool{}
	for _, l := range o.Logins {
		if l = strings.ToLower(strings.TrimSpace(l)); l != "" {
			logins[l] = true
		}
	}
	if len(logins) == 0 {
		return nil, errors.New("gateway console: no console logins")
	}
	static, err := console.New(console.Options{
		RPC: o.RPC, Forwarder: console.HostForwarder{}, Run: "-",
		Hosts: o.Hosts, Port: o.Port, HTTPS: o.HTTPS, Methods: []string{}, Log: o.Log,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("gateway console: %w", err)
	}
	return &ConsoleShares{
		o: o, logins: logins, static: static.Handler(),
		shares: map[string]*share{}, visits: map[string]*visit{}, now: time.Now,
	}, nil
}

func (c *ConsoleShares) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(c.o.Log, "gateway console: "+format+"\n", args...)
}

// peer is the connection's peer when it may use the console: a listed login
// on a node other than the host's own. Any failure refuses.
func (c *ConsoleShares) peer(ctx context.Context, remoteAddr string) (Peer, bool) {
	p, err := c.o.WhoIs(ctx, remoteAddr)
	if err != nil {
		return Peer{}, false
	}
	p.Login, p.NodeID = strings.TrimSpace(p.Login), strings.TrimSpace(p.NodeID)
	if p.NodeID == "" || !c.logins[strings.ToLower(p.Login)] {
		return Peer{}, false
	}
	nodes, err := c.o.HostNodes(ctx)
	if err != nil {
		c.logf("host nodes: %v", err)
		return Peer{}, false
	}
	for _, n := range nodes {
		if n == p.NodeID {
			return Peer{}, false
		}
	}
	return p, true
}

// Handler serves the shared consoles.
func (c *ConsoleShares) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		p, ok := c.peer(r.Context(), r.RemoteAddr)
		if !ok {
			consoleError(w, http.StatusForbidden, "forbidden", "this tailnet login may not open harness consoles")
			c.logf("refused %s %s: peer", r.Method, consoleLogPath(r.URL.Path))
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/s/"):
			c.handleShare(w, r, p, strings.TrimPrefix(r.URL.Path, "/s/"))
		case strings.HasPrefix(r.URL.Path, "/api/"):
			c.handleAPI(w, r, p)
		default:
			c.static.ServeHTTP(w, r)
		}
	})
}

func consoleError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

// consoleLogPath keeps share ids and tokens out of the log.
func consoleLogPath(p string) string {
	if strings.HasPrefix(p, "/s/") {
		return "/s/(id)"
	}
	return p
}

// validShareID is 43 base64url characters.
func validShareID(id string) bool {
	if len(id) != shareIDLen {
		return false
	}
	for _, r := range id {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
		if !ok {
			return false
		}
	}
	return true
}

// handleShare answers /s/<id>: a live share mints a visit token and
// redirects to the page with it in the fragment.
func (c *ConsoleShares) handleShare(w http.ResponseWriter, r *http.Request, p Peer, id string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		consoleError(w, http.StatusMethodNotAllowed, "bad_method", "method not allowed")
		return
	}
	if !validShareID(id) {
		consoleError(w, http.StatusNotFound, "not_found", "no such share")
		return
	}
	st, err := c.shareState(r.Context(), id)
	switch {
	case err != nil && rpcErrCode(err) == "not_found":
		consoleError(w, http.StatusNotFound, "not_found", "no such share")
		return
	case err != nil:
		c.logf("harness.share_get failed: %s", rpcErrCode(err))
		consoleError(w, http.StatusBadGateway, "serve_failed", "aiman serve did not answer; try again")
		return
	case st.State != "live":
		c.ShareEvent(id, "ended")
		consoleError(w, http.StatusNotFound, "not_found", "this share has ended")
		return
	}
	tok, err := c.mint(id, st.Run, p)
	if err != nil {
		consoleError(w, http.StatusServiceUnavailable, "unavailable", err.Error())
		return
	}
	http.Redirect(w, r, "/#token="+tok, http.StatusFound)
}

type shareStateReply struct {
	Session string `json:"session"`
	Run     string `json:"run"`
	State   string `json:"state"`
}

func (c *ConsoleShares) shareState(ctx context.Context, id string) (shareStateReply, error) {
	raw, err := c.o.RPC.Call(ctx, "harness.share_get", map[string]any{"id": id})
	if err != nil {
		return shareStateReply{}, err
	}
	var st shareStateReply
	if err := json.Unmarshal(raw, &st); err != nil {
		return shareStateReply{}, fmt.Errorf("decode share: %w", err)
	}
	return st, nil
}

// rpcErrCode is serve's refusal code, or the prefix before the first colon.
func rpcErrCode(err error) string {
	var re *usecase.RPCError
	if errors.As(err, &re) {
		return re.Code
	}
	code, _, _ := strings.Cut(err.Error(), ":")
	return strings.TrimSpace(code)
}

// newVisitToken is 32 random bytes, base64url without padding.
func newVisitToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("visit token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// mint records the share and a visit token for p.
func (c *ConsoleShares) mint(id, run string, p Peer) (string, error) {
	tok, err := newVisitToken()
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return "", errors.New("the gateway console is closing")
	}
	sh := c.shares[id]
	if sh == nil {
		sh = &share{id: id, run: run, conns: map[net.Conn]struct{}{}, fwds: map[int]*appForward{}}
		c.shares[id] = sh
	}
	c.visits[tok] = &visit{share: sh, login: p.Login, node: p.NodeID, expires: c.now().Add(VisitTokenTTL)}
	return tok, nil
}

// lookup finds a live visit token presented by p and renews it.
func (c *ConsoleShares) lookup(tok string, p Peer) (*share, bool) {
	if tok == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var v *visit
	for k, cand := range c.visits {
		if subtle.ConstantTimeCompare([]byte(k), []byte(tok)) == 1 {
			v = cand
		}
	}
	now := c.now()
	switch {
	case v == nil:
		return nil, false
	case !now.Before(v.expires):
		delete(c.visits, tok)
		return nil, false
	case !strings.EqualFold(v.login, p.Login) || v.node != p.NodeID:
		return nil, false
	case c.shares[v.share.id] != v.share:
		return nil, false
	}
	v.expires = now.Add(VisitTokenTTL)
	return v.share, true
}

// holds says login holds a live visit token for sh, renewing it.
func (c *ConsoleShares) holds(sh *share, login string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shares[sh.id] != sh {
		return false
	}
	now := c.now()
	ok := false
	for _, v := range c.visits {
		if v.share == sh && strings.EqualFold(v.login, login) && now.Before(v.expires) {
			v.expires = now.Add(VisitTokenTTL)
			ok = true
		}
	}
	return ok
}

const tokenSubprotocol = "aiman-token."

// presentedToken is the visit token from the header, or for the debugger
// WebSocket from the subprotocol offer.
func presentedToken(r *http.Request) string {
	if r.URL.Path != console.DAPPath {
		return r.Header.Get(console.TokenHeader)
	}
	for _, h := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(h, ",") {
			if tok, ok := strings.CutPrefix(strings.TrimSpace(p), tokenSubprotocol); ok {
				return tok
			}
		}
	}
	return ""
}

type testerKey struct{}

// handleAPI routes an API call by its visit token to the share's console,
// swapping the visit token for the console's own.
func (c *ConsoleShares) handleAPI(w http.ResponseWriter, r *http.Request, p Peer) {
	sh, ok := c.lookup(presentedToken(r), p)
	if !ok {
		consoleError(w, http.StatusUnauthorized, "bad_token", "open the share link again; this visit has ended or is not yours")
		return
	}
	cs, tok, err := c.consoleFor(r.Context(), sh)
	if err != nil {
		consoleError(w, http.StatusUnauthorized, "bad_token", "this share has ended")
		return
	}
	r2 := r.Clone(context.WithValue(r.Context(), testerKey{}, p.Login))
	if r.URL.Path == console.DAPPath {
		var protos []string
		for _, h := range r.Header.Values("Sec-WebSocket-Protocol") {
			for _, p := range strings.Split(h, ",") {
				p = strings.TrimSpace(p)
				if strings.HasPrefix(p, tokenSubprotocol) {
					p = tokenSubprotocol + tok
				}
				protos = append(protos, p)
			}
		}
		r2.Header.Del("Sec-WebSocket-Protocol")
		r2.Header.Set("Sec-WebSocket-Protocol", strings.Join(protos, ", "))
	} else {
		r2.Header.Set(console.TokenHeader, tok)
	}
	cs.Handler().ServeHTTP(&trackWriter{ResponseWriter: w, sh: sh}, r2)
}

// consoleFor is sh's console, made and started on first use.
func (c *ConsoleShares) consoleFor(ctx context.Context, sh *share) (*console.Server, string, error) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.ended {
		return nil, "", errors.New("share ended")
	}
	if sh.console != nil {
		return sh.console, sh.token, nil
	}
	tok, err := newVisitToken()
	if err != nil {
		return nil, "", err
	}
	cs, err := console.New(console.Options{
		RPC:       testerRPC{rpc: c.o.RPC},
		Forwarder: console.HostForwarder{},
		Run:       sh.run,
		Token:     tok,
		Log:       c.o.Log,
		Hosts:     c.o.Hosts,
		Port:      c.o.Port,
		HTTPS:     c.o.HTTPS,
		Methods:   TesterMethods,
	}, nil)
	if err != nil {
		return nil, "", err
	}
	sctx, stop := context.WithCancel(context.Background())
	go func() { _ = cs.Serve(sctx) }()
	c.waitPolled(ctx, cs, tok)
	sh.console, sh.token, sh.stop = cs, tok, stop
	return cs, tok, nil
}

// waitPolled waits, briefly, until the new console has read its run once, so
// the first action is not refused because the console shows no run yet.
func (c *ConsoleShares) waitPolled(ctx context.Context, cs *console.Server, tok string) {
	host := c.o.Hosts[0]
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	host = fmt.Sprintf("%s:%d", host, c.o.Port)
	deadline := time.Now().Add(consoleReadyWait)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/api/state", nil)
		if err != nil {
			return
		}
		req.Host = host
		req.Header.Set(console.TokenHeader, tok)
		rec := &bufferWriter{h: http.Header{}}
		cs.Handler().ServeHTTP(rec, req)
		var st struct {
			PolledAt *time.Time `json:"polled_at"`
		}
		if json.Unmarshal(rec.body, &st) == nil && st.PolledAt != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// bufferWriter is a minimal in-memory ResponseWriter.
type bufferWriter struct {
	h    http.Header
	body []byte
}

func (b *bufferWriter) Header() http.Header { return b.h }
func (b *bufferWriter) WriteHeader(int)     {}
func (b *bufferWriter) Write(p []byte) (int, error) {
	b.body = append(b.body, p...)
	return len(p), nil
}

// testerRPC calls serve for a shared console and names the visitor as the
// tester on every verdict.
type testerRPC struct{ rpc console.RPC }

func (t testerRPC) Call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	if method == "harness.verdict" {
		login, _ := ctx.Value(testerKey{}).(string)
		if login == "" {
			return nil, &usecase.RPCError{Code: "forbidden", Message: "a verdict needs the tester's login"}
		}
		withTester := make(map[string]any, len(params)+1)
		for k, v := range params {
			withTester[k] = v
		}
		withTester["tester"] = login
		params = withTester
	}
	return t.rpc.Call(ctx, method, params)
}

// trackWriter records a hijacked connection (a debugger bridge) on its share,
// so ending the share closes it.
type trackWriter struct {
	http.ResponseWriter
	sh *share
}

func (w *trackWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *trackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("gateway console: the connection cannot be hijacked")
	}
	conn, brw, err := h.Hijack()
	if err != nil {
		return nil, nil, err
	}
	tc, ok := w.sh.track(conn)
	if !ok {
		_ = conn.Close()
		return nil, nil, errors.New("gateway console: the share has ended")
	}
	return tc, brw, nil
}

// trackedConn leaves its share's set when closed.
type trackedConn struct {
	net.Conn
	sh   *share
	once sync.Once
}

func (t *trackedConn) Close() error {
	t.once.Do(func() { t.sh.untrack(t) })
	return t.Conn.Close()
}

// track adds conn to the share's set; false once the share has ended.
func (sh *share) track(conn net.Conn) (net.Conn, bool) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.ended {
		return nil, false
	}
	tc := &trackedConn{Conn: conn, sh: sh}
	sh.conns[tc] = struct{}{}
	return tc, true
}

func (sh *share) untrack(conn net.Conn) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	delete(sh.conns, conn)
}

// ShareEvent applies serve's harness_share event: ended ends the share.
func (c *ConsoleShares) ShareEvent(id, state string) {
	if state == "ended" {
		c.end(id)
	}
}

// end drops the share's visit tokens and closes its console, bridges and
// forwards.
func (c *ConsoleShares) end(id string) {
	c.mu.Lock()
	sh := c.shares[id]
	delete(c.shares, id)
	for k, v := range c.visits {
		if v.share.id == id {
			delete(c.visits, k)
		}
	}
	c.mu.Unlock()
	if sh != nil {
		sh.close()
	}
}

// close ends the share's console and connections at once.
func (sh *share) close() {
	sh.mu.Lock()
	sh.ended = true
	stop := sh.stop
	conns := make([]net.Conn, 0, len(sh.conns))
	for cn := range sh.conns {
		conns = append(conns, cn)
	}
	fwds := sh.fwds
	sh.fwds = map[int]*appForward{}
	sh.mu.Unlock()
	if stop != nil {
		stop()
	}
	for _, f := range fwds {
		f.close()
	}
	for _, cn := range conns {
		_ = cn.Close()
	}
}

// PollOnce asks serve about each live share: an ended or unknown share ends,
// and a live one's forwards follow its run.
func (c *ConsoleShares) PollOnce(ctx context.Context) {
	c.mu.Lock()
	now := c.now()
	for k, v := range c.visits {
		if !now.Before(v.expires) {
			delete(c.visits, k)
		}
	}
	list := make([]*share, 0, len(c.shares))
	for _, sh := range c.shares {
		list = append(list, sh)
	}
	c.mu.Unlock()
	for _, sh := range list {
		st, err := c.shareState(ctx, sh.id)
		if err != nil {
			if rpcErrCode(err) == "not_found" {
				c.end(sh.id)
				continue
			}
			c.logf("harness.share_get failed: %s", rpcErrCode(err))
			continue
		}
		if st.State != "live" {
			c.end(sh.id)
			continue
		}
		c.syncRun(ctx, sh)
	}
}

// syncRun reads the share's run and keeps its forwards in step.
func (c *ConsoleShares) syncRun(ctx context.Context, sh *share) {
	raw, err := c.o.RPC.Call(ctx, "harness.status", map[string]any{"run": sh.run})
	if err != nil {
		if rpcErrCode(err) != harness.CodeNotRunning {
			c.logf("harness.status failed: %s", rpcErrCode(err))
		}
		c.syncForwards(sh, nil)
		return
	}
	var reply struct {
		Run *harness.Run `json:"run"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil || reply.Run == nil || reply.Run.ID != sh.run {
		c.syncForwards(sh, nil)
		return
	}
	if reply.Run.State != harness.StateReady {
		c.syncForwards(sh, nil)
		return
	}
	c.syncForwards(sh, reply.Run.Ports)
}

// Run polls every SharePollEvery until ctx ends.
func (c *ConsoleShares) Run(ctx context.Context) error {
	t := time.NewTicker(SharePollEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			c.PollOnce(ctx)
		}
	}
}

// Close ends every share.
func (c *ConsoleShares) Close() error {
	c.mu.Lock()
	c.closed = true
	ids := make([]string, 0, len(c.shares))
	for id := range c.shares {
		ids = append(ids, id)
	}
	c.mu.Unlock()
	for _, id := range ids {
		c.end(id)
	}
	return nil
}
