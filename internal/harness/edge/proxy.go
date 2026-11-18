package edge

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Proxy defaults.
const (
	// ProxyPort is the proxy's port on the edge's inner address.
	ProxyPort = 3128
	// handshakeTimeout bounds the wait for a tunnel's first byte and its TLS
	// handshake.
	handshakeTimeout = 10 * time.Second
	// stubDrainBytes bounds how much of a request body the proxy reads when
	// it answers without an upstream.
	stubDrainBytes = 8 << 20
	// idleConnTimeout closes a client connection that carries no request.
	idleConnTimeout = 2 * time.Minute
)

// Stub is a fixed answer for a host and path, from a scenario's stubs.
type Stub struct {
	Host   string `json:"host"`
	Path   string `json:"path"`
	Status int    `json:"status"`
	Body   []byte `json:"body"`
}

// ProxyConfig configures the edge proxy.
type ProxyConfig struct {
	// Online forwards a request no stub answers; offline answers it 502.
	Online bool
	// CA signs the leaf certificates of intercepted TLS; required.
	CA *CA
	// Stubs answer matching requests in either mode.
	Stubs []Stub
	// BlockSuffixes are host suffixes refused in either mode, before any
	// stub: a host equal to one or ending in "."+one. A run with an AWS
	// emulator blocks real AWS this way.
	BlockSuffixes []string
	// BodyBytes is how much of each body a flow keeps; zero is
	// DefaultBodyBytes.
	BodyBytes int
	// RootCAs verifies upstream servers in an online run; nil is the
	// system pool.
	RootCAs *x509.CertPool
	// DialUpstream reaches an upstream in an online run; nil is DialPublic,
	// which refuses every address that is not a public unicast one.
	DialUpstream func(ctx context.Context, network, addr string) (net.Conn, error)
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Proxy is the edge's HTTP forward proxy. It reads HTTPS by terminating each
// CONNECT tunnel with a leaf signed by the run's CA, answers from a stub, or
// forwards online, and records every request as a Flow.
type Proxy struct {
	cfg       ProxyConfig
	store     flowStore
	transport *http.Transport
	rp        *httputil.ReverseProxy
	front     *http.Server
	inner     *http.Server
	tunnels   *connQueue
	closeOnce sync.Once
}

type ctxKey int

const (
	keyTarget ctxKey = iota
	keyFlow
)

// NewProxy checks cfg and returns a proxy that serves nothing yet.
func NewProxy(cfg ProxyConfig) (*Proxy, error) {
	if cfg.CA == nil {
		return nil, errors.New("edge proxy needs the run's CA")
	}
	if cfg.BodyBytes <= 0 {
		cfg.BodyBytes = DefaultBodyBytes
	}
	cfg.BodyBytes = min(cfg.BodyBytes, MaxBodyBytes)
	if cfg.DialUpstream == nil {
		cfg.DialUpstream = DialPublic
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	for i := range cfg.BlockSuffixes {
		cfg.BlockSuffixes[i] = strings.Trim(strings.ToLower(cfg.BlockSuffixes[i]), ".")
	}
	cfg.BlockSuffixes = slices.DeleteFunc(cfg.BlockSuffixes, func(s string) bool { return s == "" })
	for i := range cfg.Stubs {
		cfg.Stubs[i].Host = strings.ToLower(cfg.Stubs[i].Host)
		if cfg.Stubs[i].Status == 0 {
			cfg.Stubs[i].Status = http.StatusOK
		}
	}
	p := &Proxy{cfg: cfg, tunnels: newConnQueue()}
	p.transport = &http.Transport{
		DialContext:           cfg.DialUpstream,
		TLSClientConfig:       &tls.Config{RootCAs: cfg.RootCAs, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   handshakeTimeout,
		ResponseHeaderTimeout: 2 * time.Minute,
	}
	p.rp = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = pr.In.Context().Value(keyFlow).(*flowCtx).target
			pr.Out.Host = pr.In.Host
		},
		Transport:      p.transport,
		FlushInterval:  -1,
		ModifyResponse: p.modifyResponse,
		ErrorHandler:   p.upstreamError,
		ErrorLog:       log.New(io.Discard, "", 0),
	}
	p.front = &http.Server{Handler: http.HandlerFunc(p.serveFront), ReadHeaderTimeout: handshakeTimeout, IdleTimeout: idleConnTimeout, ErrorLog: log.New(io.Discard, "", 0)}
	p.inner = &http.Server{
		Handler:           http.HandlerFunc(p.serveTunnel),
		ReadHeaderTimeout: handshakeTimeout,
		IdleTimeout:       idleConnTimeout,
		ErrorLog:          log.New(io.Discard, "", 0),
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if tc, ok := c.(*tls.Conn); ok {
				if bc, ok := tc.NetConn().(*tunnelConn); ok {
					return context.WithValue(ctx, keyTarget, bc)
				}
			}
			return ctx
		},
	}
	return p, nil
}

// Serve answers proxy clients on ln until Close.
func (p *Proxy) Serve(ln net.Listener) error {
	errc := make(chan error, 1)
	go func() { errc <- p.inner.Serve(p.tunnels) }()
	err := p.front.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

// Close stops the proxy and every connection it holds.
func (p *Proxy) Close() error {
	p.closeOnce.Do(func() {
		_ = p.front.Close()
		_ = p.inner.Close()
		_ = p.tunnels.Close()
		p.transport.CloseIdleConnections()
	})
	return nil
}

// Online reports the mode.
func (p *Proxy) Online() bool { return p.cfg.Online }

// Network is "online" or "offline".
func (p *Proxy) Network() string {
	if p.cfg.Online {
		return "online"
	}
	return "offline"
}

// CACertPEM is the run CA's certificate.
func (p *Proxy) CACertPEM() []byte { return p.cfg.CA.CertPEM() }

// Capture reads flows with an ID above after, oldest first.
func (p *Proxy) Capture(after int64, limit int) ([]Flow, CaptureInfo) {
	flows, info := p.store.read(after, limit, p.cfg.Now())
	info.Network = p.Network()
	return flows, info
}

// serveFront answers the proxy port: CONNECT tunnels and absolute-form
// plain HTTP requests.
func (p *Proxy) serveFront(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodConnect:
		p.connect(w, r)
	case r.URL.IsAbs() && r.URL.Scheme == "http" && r.URL.Host != "":
		target := *r.URL
		target.User = nil
		p.forward(w, r, &target, false)
	default:
		http.Error(w, "aiman edge proxy: send requests through HTTP_PROXY or HTTPS_PROXY", http.StatusBadRequest)
	}
}

// tunnelConn is a hijacked CONNECT connection: the bytes the server already
// buffered come first, and it carries the tunnel's target and client.
type tunnelConn struct {
	net.Conn
	r      *bufio.Reader
	target string
	client string
}

func (c *tunnelConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// connect takes a CONNECT tunnel. A TLS client gets a leaf for its server
// name and its requests go to serveTunnel; anything else is refused, because
// the proxy captures HTTP and HTTPS only.
func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	host, port, err := net.SplitHostPort(target)
	if err != nil || host == "" {
		http.Error(w, "aiman edge proxy: CONNECT needs host:port", http.StatusBadRequest)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "aiman edge proxy: cannot take the tunnel", http.StatusInternalServerError)
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return
	}
	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		_ = conn.Close()
		return
	}
	tc := &tunnelConn{Conn: conn, r: brw.Reader, target: target, client: clientIP(r.RemoteAddr)}
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	first, err := tc.r.Peek(1)
	if err != nil || first[0] != 0x16 {
		p.recordTunnel(tc, host, port, OutcomeRefused, "the tunnel did not start TLS; the edge proxy carries HTTP and HTTPS only")
		_ = conn.Close()
		return
	}
	tlsConn := tls.Server(tc, &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
		GetCertificate: func(hi *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := hi.ServerName
			if name == "" {
				name = host
			}
			return p.cfg.CA.Leaf(name)
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	defer cancel()
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		p.recordTunnel(tc, host, port, OutcomeError, "tls handshake: "+err.Error())
		_ = conn.Close()
		return
	}
	_ = conn.SetDeadline(time.Time{})
	if !p.tunnels.push(tlsConn) {
		_ = tlsConn.Close()
	}
}

// recordTunnel stores a CONNECT that never carried a request.
func (p *Proxy) recordTunnel(tc *tunnelConn, host, port, outcome, msg string) {
	u := &url.URL{Scheme: "https", Host: hostPort(host, port, "443")}
	f := &Flow{Start: p.cfg.Now(), Client: tc.client, Method: http.MethodConnect, URL: u.String(), Host: host, TLS: true, Outcome: outcome, Error: msg}
	f.Redacted = RedactFlow(f)
	p.store.add(f)
}

// serveTunnel answers a request read inside a CONNECT tunnel.
func (p *Proxy) serveTunnel(w http.ResponseWriter, r *http.Request) {
	tc, _ := r.Context().Value(keyTarget).(*tunnelConn)
	if tc == nil {
		http.Error(w, "aiman edge proxy: no tunnel target", http.StatusBadGateway)
		return
	}
	host, port, _ := net.SplitHostPort(tc.target)
	u := &url.URL{Scheme: "https", Host: hostPort(host, port, "443"), Path: r.URL.Path, RawPath: r.URL.RawPath, RawQuery: r.URL.RawQuery}
	r.RemoteAddr = tc.client
	p.forward(w, r, u, true)
}

func hostPort(host, port, def string) string {
	if port == def || port == "" {
		if strings.Contains(host, ":") {
			return "[" + host + "]"
		}
		return host
	}
	return net.JoinHostPort(host, port)
}

func clientIP(remote string) string {
	if h, _, err := net.SplitHostPort(remote); err == nil {
		return h
	}
	return remote
}

// flowCtx follows one request from forward to its response.
type flowCtx struct {
	flow   *Flow
	target *url.URL
	req    *bodyCapture
	once   sync.Once
}

// forward records the request, then answers it from a stub, with the offline
// refusal, or from upstream.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, target *url.URL, isTLS bool) {
	f := &Flow{
		Start: p.cfg.Now(), Client: clientIP(r.RemoteAddr), Method: r.Method, URL: target.String(),
		Host: strings.ToLower(target.Hostname()), Proto: r.Proto, TLS: isTLS, Outcome: OutcomePending,
		RequestHeaders: r.Header.Clone(),
	}
	f.Redacted = RedactFlow(f)
	p.store.add(f)
	fc := &flowCtx{flow: f, target: target, req: newBodyCapture(p.cfg.BodyBytes)}
	if r.Body != nil && r.Body != http.NoBody {
		r.Body = &captureReader{rc: r.Body, c: fc.req}
	}
	if p.blocked(f.Host) {
		drain(r.Body)
		body := []byte(BlockedAWSMessage + "\n")
		h := http.Header{"Content-Type": {"text/plain; charset=utf-8"}}
		writeFixed(w, h, http.StatusBadGateway, body)
		p.finish(fc, http.StatusBadGateway, OutcomeRefused, h, body, BlockedAWSMessage)
		return
	}
	if st := p.stub(f.Host, target.Path); st != nil {
		drain(r.Body)
		h := http.Header{"Content-Type": {stubContentType(st.Body)}, "X-Aiman-Stub": {"1"}}
		writeFixed(w, h, st.Status, st.Body)
		p.finish(fc, st.Status, OutcomeStub, h, st.Body, "")
		return
	}
	if !p.cfg.Online {
		drain(r.Body)
		body := []byte(fmt.Sprintf("aiman: offline sandbox, no stub for %s%s\n", target.Host, target.EscapedPath()))
		h := http.Header{"Content-Type": {"text/plain; charset=utf-8"}}
		writeFixed(w, h, http.StatusBadGateway, body)
		p.finish(fc, http.StatusBadGateway, OutcomeOffline, h, body, "")
		return
	}
	p.rp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyFlow, fc)))
}

// BlockedAWSMessage answers a request to a blocked host.
const BlockedAWSMessage = "aiman: real AWS is blocked in a run with an AWS emulator; use AWS_ENDPOINT_URL"

// blocked reports whether host (lower case, no port) is a BlockSuffixes
// host. A trailing dot is ignored.
func (p *Proxy) blocked(host string) bool {
	host = strings.TrimSuffix(host, ".")
	for _, s := range p.cfg.BlockSuffixes {
		if host == s || strings.HasSuffix(host, "."+s) {
			return true
		}
	}
	return false
}

func (p *Proxy) stub(host, path string) *Stub {
	for i := range p.cfg.Stubs {
		if p.cfg.Stubs[i].Host == host && p.cfg.Stubs[i].Path == path {
			return &p.cfg.Stubs[i]
		}
	}
	return nil
}

func stubContentType(body []byte) string {
	t := strings.TrimSpace(string(body[:min(len(body), 512)]))
	if strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		return "application/json"
	}
	return http.DetectContentType(body)
}

func writeFixed(w http.ResponseWriter, h http.Header, status int, body []byte) {
	for k, v := range h {
		w.Header()[k] = v
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body) //nolint:gosec // G705: a stub body or a plain-text refusal for the sandbox's HTTP client, not a page a browser renders from this origin
}

func drain(rc io.ReadCloser) {
	if rc == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(rc, stubDrainBytes))
	_ = rc.Close()
}

// modifyResponse records the upstream's status and headers and captures the
// body as the client reads it; the flow ends when the body does.
func (p *Proxy) modifyResponse(resp *http.Response) error {
	fc := resp.Request.Context().Value(keyFlow).(*flowCtx)
	p.store.update(fc.flow, func(f *Flow) {
		f.Status = resp.StatusCode
		f.ResponseHeaders, _ = redactHeaders(resp.Header.Clone())
	})
	if resp.StatusCode == http.StatusSwitchingProtocols || resp.Body == nil || resp.Body == http.NoBody {
		p.finish(fc, resp.StatusCode, OutcomeUpstream, resp.Header, nil, "")
		return nil
	}
	c := newBodyCapture(p.cfg.BodyBytes)
	resp.Body = &captureReader{rc: resp.Body, c: c, done: func(err error) {
		msg := ""
		if err != nil && !errors.Is(err, io.EOF) {
			msg = "response body: " + err.Error()
		}
		raw, total, cut := c.snapshot()
		p.finishBody(fc, resp.StatusCode, OutcomeUpstream, resp.Header, raw, total, cut, msg)
	}}
	return nil
}

// upstreamError answers 502 when the upstream cannot be reached, and records
// a refused address as refused.
func (p *Proxy) upstreamError(w http.ResponseWriter, r *http.Request, err error) {
	fc := r.Context().Value(keyFlow).(*flowCtx)
	outcome := OutcomeError
	if errors.Is(err, ErrNotPublic) {
		outcome = OutcomeRefused
	}
	body := []byte("aiman edge proxy: " + err.Error() + "\n")
	h := http.Header{"Content-Type": {"text/plain; charset=utf-8"}}
	writeFixed(w, h, http.StatusBadGateway, body)
	p.finish(fc, http.StatusBadGateway, outcome, h, body, err.Error())
}

func (p *Proxy) finish(fc *flowCtx, status int, outcome string, h http.Header, body []byte, msg string) {
	p.finishBody(fc, status, outcome, h, body, int64(len(body)), false, msg)
}

// finishBody ends a flow once: duration, status, both bodies, redaction.
func (p *Proxy) finishBody(fc *flowCtx, status int, outcome string, h http.Header, body []byte, total int64, cut bool, msg string) {
	fc.once.Do(func() {
		reqRaw, reqTotal, reqCut := fc.req.snapshot()
		reqText, reqBin := bodyText(reqRaw, fc.flow.RequestHeaders.Get("Content-Encoding"), p.cfg.BodyBytes)
		respText, respBin := bodyText(body, h.Get("Content-Encoding"), p.cfg.BodyBytes)
		p.store.update(fc.flow, func(f *Flow) {
			f.Duration = float64(p.cfg.Now().Sub(f.Start).Microseconds()) / 1000
			f.Status, f.Outcome, f.Error = status, outcome, msg
			if f.ResponseHeaders == nil {
				f.ResponseHeaders, _ = redactHeaders(h.Clone())
			}
			f.RequestBody, f.RequestBodyBytes, f.RequestTruncated, f.RequestBinary = reqText, reqTotal, reqCut, reqBin
			f.ResponseBody, f.ResponseBodyBytes, f.ResponseTruncated, f.ResponseBinary = respText, total, cut, respBin
			f.Redacted += RedactFlow(f)
		})
	})
}

// ErrNotPublic refuses an upstream address that is not public unicast.
var ErrNotPublic = errors.New("the edge proxy reaches public addresses only")

// DialPublic resolves addr's host to IPv4 addresses and dials the first one,
// refusing when any of them is loopback, private, link-local, shared,
// multicast or otherwise not public, so an online run cannot reach the host,
// its networks or a cloud metadata service through the proxy.
func DialPublic(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip.Unmap()}
	} else {
		ips, err = net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
		if err != nil {
			return nil, err
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%s has no IPv4 address", host)
	}
	for _, ip := range ips {
		if !PublicAddr(ip) {
			return nil, fmt.Errorf("%s resolves to %s: %w", host, ip, ErrNotPublic)
		}
	}
	d := net.Dialer{Timeout: 15 * time.Second}
	var last error
	for _, ip := range ips {
		c, err := d.DialContext(ctx, "tcp4", net.JoinHostPort(ip.String(), port))
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

// nonPublic lists IPv4 ranges that are not public beyond what netip's
// predicates cover: this network, shared address space, IETF protocol
// assignments, documentation, benchmarking and reserved space.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

// PublicAddr reports whether ip is a public unicast address.
func PublicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// connQueue is a listener fed with connections the proxy accepted itself.
type connQueue struct {
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
}

func newConnQueue() *connQueue {
	return &connQueue{ch: make(chan net.Conn), done: make(chan struct{})}
}

func (q *connQueue) push(c net.Conn) bool {
	select {
	case q.ch <- c:
		return true
	case <-q.done:
		return false
	}
}

func (q *connQueue) Accept() (net.Conn, error) {
	select {
	case c := <-q.ch:
		return c, nil
	case <-q.done:
		return nil, net.ErrClosed
	}
}

func (q *connQueue) Close() error {
	q.once.Do(func() { close(q.done) })
	return nil
}

func (q *connQueue) Addr() net.Addr { return &net.TCPAddr{} }
