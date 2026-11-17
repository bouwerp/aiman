package edge

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testProxy serves a proxy on 127.0.0.1 and returns it with its URL.
func testProxy(t *testing.T, cfg ProxyConfig) (*Proxy, *url.URL) {
	t.Helper()
	if cfg.CA == nil {
		ca, err := NewCA(time.Now())
		if err != nil {
			t.Fatal(err)
		}
		cfg.CA = ca
	}
	p, err := NewProxy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = p.Serve(ln) }()
	t.Cleanup(func() { _ = p.Close() })
	return p, &url.URL{Scheme: "http", Host: ln.Addr().String()}
}

// client uses the proxy and trusts the run CA alone, as the app does with
// SSL_CERT_FILE.
func client(t *testing.T, p *Proxy, proxy *url.URL) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(p.CACertPEM()) {
		t.Fatal("ca certificate does not parse")
	}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:             http.ProxyURL(proxy),
			TLSClientConfig:   &tls.Config{RootCAs: pool},
			ForceAttemptHTTP2: true,
		},
	}
}

// dialTo sends every upstream dial to addr, standing in for DNS.
func dialTo(addr string) func(ctx context.Context, network, _ string) (net.Conn, error) {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
}

func refuseDial(t *testing.T) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(context.Context, string, string) (net.Conn, error) {
		t.Errorf("an offline proxy dialed upstream")
		return nil, ErrNotPublic
	}
}

// waitFlows reads the store until want flows are finished.
func waitFlows(t *testing.T, p *Proxy, want int) []Flow {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		flows, _ := p.Capture(0, MaxCaptureLimit)
		done := 0
		for _, f := range flows {
			if f.Outcome != OutcomePending {
				done++
			}
		}
		if done >= want {
			return flows
		}
		if time.Now().After(deadline) {
			t.Fatalf("want %d finished flows, have %+v", want, flows)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func get(t *testing.T, c *http.Client, method, u string, body io.Reader, h http.Header) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range h {
		req.Header[k] = v
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, u, err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(b)
}

func TestProxyRecords(t *testing.T) {
	up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"echo":"` + string(b) + `","path":"` + r.URL.Path + `","host":"` + r.Host + `"}`))
	}))
	up.EnableHTTP2 = true
	up.StartTLS()
	defer up.Close()
	roots := x509.NewCertPool()
	roots.AddCert(up.Certificate())
	p, pu := testProxy(t, ProxyConfig{Online: true, RootCAs: roots, DialUpstream: dialTo(up.Listener.Addr().String())})
	c := client(t, p, pu)

	resp, body := get(t, c, http.MethodPost, "https://example.com/v1/orders?id=42", strings.NewReader("hello"), nil)
	if resp.StatusCode != http.StatusCreated || !strings.Contains(body, `"echo":"hello"`) || !strings.Contains(body, `"host":"example.com"`) {
		t.Fatalf("https through the proxy: %d %s", resp.StatusCode, body)
	}
	if resp.Proto != "HTTP/2.0" {
		t.Errorf("client spoke %s to the proxy's leaf, want HTTP/2.0", resp.Proto)
	}
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("plain " + r.URL.Path))
	}))
	defer plain.Close()
	p2, pu2 := testProxy(t, ProxyConfig{Online: true, DialUpstream: dialTo(plain.Listener.Addr().String())})
	if _, body := get(t, client(t, p2, pu2), http.MethodGet, "http://api.internal.example/x", nil, nil); body != "plain /x" {
		t.Fatalf("plain http through the proxy: %q", body)
	}

	flows := waitFlows(t, p, 1)
	f := flows[0]
	if f.ID != 1 || f.Method != http.MethodPost || f.URL != "https://example.com/v1/orders?id=42" || f.Host != "example.com" ||
		!f.TLS || f.Status != http.StatusCreated || f.Outcome != OutcomeUpstream {
		t.Fatalf("flow = %+v", f)
	}
	if f.RequestBody != "hello" || f.RequestBodyBytes != 5 || !strings.Contains(f.ResponseBody, `"echo":"hello"`) ||
		f.ResponseHeaders.Get("Content-Type") != "application/json" || f.Duration <= 0 || f.Start.IsZero() {
		t.Fatalf("flow bodies and timing = %+v", f)
	}
	pf := waitFlows(t, p2, 1)[0]
	if pf.TLS || pf.Method != http.MethodGet || pf.URL != "http://api.internal.example/x" || pf.ResponseBody != "plain /x" {
		t.Fatalf("plain flow = %+v", pf)
	}
	if _, info := p.Capture(1, 10); info.Next != 1 || info.Total != 1 || info.Network != "online" {
		t.Fatalf("capture after the last flow = %+v", info)
	}
}

func TestProxyKeepsBodyPrefix(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("a"), 5000))
	}))
	defer up.Close()
	p, pu := testProxy(t, ProxyConfig{Online: true, BodyBytes: 100, DialUpstream: dialTo(up.Listener.Addr().String())})
	if _, body := get(t, client(t, p, pu), http.MethodGet, "http://big.example/", nil, nil); len(body) != 5000 {
		t.Fatalf("client got %d bytes, want the whole body", len(body))
	}
	f := waitFlows(t, p, 1)[0]
	if len(f.ResponseBody) != 100 || f.ResponseBodyBytes != 5000 || !f.ResponseTruncated {
		t.Fatalf("flow kept %d of %d bytes, truncated %v", len(f.ResponseBody), f.ResponseBodyBytes, f.ResponseTruncated)
	}
}

func TestStubOffline(t *testing.T) {
	p, pu := testProxy(t, ProxyConfig{
		DialUpstream: refuseDial(t),
		Stubs:        []Stub{{Host: "API.stripe.com", Path: "/v1/refunds", Status: 200, Body: []byte(`{"id":"re_1","status":"succeeded"}`)}},
	})
	c := client(t, p, pu)
	resp, body := get(t, c, http.MethodPost, "https://api.stripe.com/v1/refunds", strings.NewReader("amount=100"), nil)
	if resp.StatusCode != 200 || body != `{"id":"re_1","status":"succeeded"}` || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("stub answer: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	resp, body = get(t, c, http.MethodGet, "https://api.stripe.com/v1/charges", nil, nil)
	if resp.StatusCode != http.StatusBadGateway || body != "aiman: offline sandbox, no stub for api.stripe.com/v1/charges\n" {
		t.Fatalf("offline answer: %d %q", resp.StatusCode, body)
	}
	resp, body = get(t, c, http.MethodGet, "http://example.com/x", nil, nil)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "no stub for example.com/x") {
		t.Fatalf("offline plain http: %d %q", resp.StatusCode, body)
	}
	flows := waitFlows(t, p, 3)
	if flows[0].Outcome != OutcomeStub || flows[0].RequestBody != "amount=100" || flows[0].Status != 200 {
		t.Fatalf("stub flow = %+v", flows[0])
	}
	if flows[1].Outcome != OutcomeOffline || flows[1].Status != http.StatusBadGateway || flows[2].Outcome != OutcomeOffline {
		t.Fatalf("offline flows = %+v / %+v", flows[1], flows[2])
	}
	if _, info := p.Capture(0, 1); info.Network != "offline" || !info.More || info.Next != 1 {
		t.Fatalf("paged capture = %+v", info)
	}
}

func TestRedactsAuthHeader(t *testing.T) {
	const secret = "s3cr3t-value-123"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Errorf("upstream lost the Authorization header: %q", r.Header.Get("Authorization"))
		}
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: secret})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + secret + `","user":{"name":"ann","password":"` + secret + `"}}`))
	}))
	defer up.Close()
	p, pu := testProxy(t, ProxyConfig{Online: true, DialUpstream: dialTo(up.Listener.Addr().String())})
	h := http.Header{
		"Authorization": {"Bearer " + secret},
		"Cookie":        {"sid=" + secret},
		"X-Api-Key":     {secret},
		"X-Auth-Token":  {secret},
		"Content-Type":  {"application/json"},
	}
	get(t, client(t, p, pu), http.MethodPost, "http://svc.example/login?api_key="+secret+"&q=ok",
		strings.NewReader(`{"email":"a@b.c","password":"`+secret+`"}`), h)
	f := waitFlows(t, p, 1)[0]
	raw, _ := json.Marshal(f)
	if strings.Contains(string(raw), secret) {
		t.Fatalf("flow keeps the secret: %s", raw)
	}
	for _, k := range []string{"Authorization", "Cookie", "X-Api-Key", "X-Auth-Token"} {
		if f.RequestHeaders.Get(k) != "[redacted]" {
			t.Errorf("request header %s = %q", k, f.RequestHeaders.Get(k))
		}
	}
	if f.ResponseHeaders.Get("Set-Cookie") != "[redacted]" {
		t.Errorf("Set-Cookie = %q", f.ResponseHeaders.Get("Set-Cookie"))
	}
	if !strings.Contains(f.RequestBody, `"email":"a@b.c"`) || !strings.Contains(f.ResponseBody, `"name":"ann"`) || !strings.Contains(f.URL, "q=ok") {
		t.Fatalf("redaction removed more than secrets: %s", raw)
	}
	// Serve redacts again; a second pass changes nothing.
	again := f
	RedactFlow(&again)
	if b, _ := json.Marshal(again); !bytes.Equal(b, raw) {
		t.Fatalf("second redaction changed the flow:\n%s\n%s", raw, b)
	}
}

func TestRedactBodyCutAtLimit(t *testing.T) {
	out, n := redactBody(`{"user":"a","token":"abc123","password":"hunter2`)
	if n < 2 || strings.Contains(out, "abc123") || strings.Contains(out, "hunter2") {
		t.Fatalf("cut JSON body = %q (%d)", out, n)
	}
	out, n = redactBody("user=a&password=hunter2&next=/x")
	if n < 1 || strings.Contains(out, "hunter2") || !strings.Contains(out, "next=/x") {
		t.Fatalf("form body = %q (%d)", out, n)
	}
}

func TestMITMWithRunCA(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("upstream"))
	}))
	defer up.Close()
	roots := x509.NewCertPool()
	roots.AddCert(up.Certificate())
	p, pu := testProxy(t, ProxyConfig{Online: true, RootCAs: roots, DialUpstream: dialTo(up.Listener.Addr().String())})

	// The app trusts the run CA: the leaf the proxy shows verifies for the
	// name it asked for, and is signed by the run CA.
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(p.CACertPEM())
	var seen *x509.Certificate
	c := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		Proxy: http.ProxyURL(pu),
		TLSClientConfig: &tls.Config{RootCAs: pool, VerifyConnection: func(cs tls.ConnectionState) error {
			seen = cs.PeerCertificates[0]
			return nil
		}},
	}}
	if _, body := get(t, c, http.MethodGet, "https://example.com/", nil, nil); body != "upstream" {
		t.Fatalf("body %q", body)
	}
	if seen == nil || seen.Subject.CommonName != "example.com" || seen.CheckSignatureFrom(p.cfg.CA.Certificate()) != nil {
		t.Fatalf("leaf = %+v", seen)
	}
	ca := p.cfg.CA.Certificate()
	if !ca.IsCA || ca.NotAfter.Sub(ca.NotBefore) > CALifetime+caBackdate || ca.PublicKeyAlgorithm != x509.ECDSA {
		t.Fatalf("ca = %+v", ca)
	}

	// A client that does not trust the run CA (a pinned client) fails, and
	// capture shows the failed handshake.
	strict := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{RootCAs: x509.NewCertPool()},
	}}
	if _, err := strict.Get("https://pinned.example.net/"); err == nil {
		t.Fatal("a client that does not trust the run CA got through")
	}
	flows := waitFlows(t, p, 2)
	f := flows[1]
	if f.Method != http.MethodConnect || f.Outcome != OutcomeError || !strings.Contains(f.Error, "tls handshake") || f.Host != "pinned.example.net" {
		t.Fatalf("failed handshake flow = %+v", f)
	}
}

func TestProxyRefusesNonTLSTunnel(t *testing.T) {
	p, pu := testProxy(t, ProxyConfig{Online: true, DialUpstream: refuseDial(t)})
	conn, err := net.Dial("tcp", pu.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("CONNECT db.example:5432 HTTP/1.1\r\nHost: db.example:5432\r\n\r\nraw bytes"))
	f := waitFlows(t, p, 1)[0]
	if f.Outcome != OutcomeRefused || f.Method != http.MethodConnect {
		t.Fatalf("flow = %+v", f)
	}
}

func TestPublicAddr(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.231.0.2", "172.17.0.1", "192.168.1.1", "169.254.169.254", "100.100.1.1", "0.0.0.0", "224.0.0.1", "::1", "fd00::1", "198.18.0.1"} {
		if PublicAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s counted as public", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "93.184.216.34", "2606:4700::1111"} {
		if !PublicAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s counted as not public", s)
		}
	}
	if _, err := DialPublic(context.Background(), "tcp", "127.0.0.1:1"); err == nil || !strings.Contains(err.Error(), ErrNotPublic.Error()) {
		t.Fatalf("DialPublic to loopback: %v", err)
	}
}

func TestOnlineRefusesPrivateUpstream(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the proxy reached a loopback upstream")
	}))
	defer up.Close()
	p, pu := testProxy(t, ProxyConfig{Online: true})
	resp, _ := get(t, client(t, p, pu), http.MethodGet, up.URL+"/", nil, nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if f := waitFlows(t, p, 1)[0]; f.Outcome != OutcomeRefused {
		t.Fatalf("flow = %+v", f)
	}
}

// TestCAKeyNeverOnHost runs the CA and the control socket as the edge does
// and checks that nothing written to disk, and nothing the control socket
// answers, holds a private key.
func TestCAKeyNeverOnHost(t *testing.T) {
	root := shortTempDir(t)
	caDir := filepath.Join(root, "ca")
	ctlDir := filepath.Join(root, "control")
	for _, d := range []string{caDir, ctlDir} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ca, err := NewCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := ca.WriteCert(caDir); err != nil {
		t.Fatal(err)
	}
	p, err := NewProxy(ProxyConfig{CA: ca})
	if err != nil {
		t.Fatal(err)
	}
	e := newTestEdge(t)
	e.SetProxy(p)
	sock := filepath.Join(ctlDir, SocketName)
	ln, err := ListenControl(sock)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = e.ServeControl(ln) }()
	t.Cleanup(func() { _ = ln.Close() })
	c := Client{Socket: sock, Token: testToken}
	cert, err := c.CA(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Capture(context.Background(), 0, 10); err != nil {
		t.Fatal(err)
	}
	blocks := 0
	for rest := cert; ; {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		blocks++
		if b.Type != "CERTIFICATE" {
			t.Fatalf("control CA answer holds a %s block", b.Type)
		}
	}
	if blocks != 1 {
		t.Fatalf("control CA answer holds %d blocks", blocks)
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&fs.ModeSocket != 0 {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(b, []byte("PRIVATE KEY")) {
			t.Errorf("%s holds a private key", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(caDir, CACertName)); !bytes.Equal(got, cert) {
		t.Fatal("ca.pem differs from the control socket's certificate")
	}
}
