package console

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/bouwerp/aiman/internal/usecase"
)

const testToken = "tok-0123456789abcdef0123456789abcdef"

type rpcCall struct {
	method string
	params map[string]any
}

type fakeRPC struct {
	mu     sync.Mutex
	calls  []rpcCall
	answer func(method string, params map[string]any) (json.RawMessage, error)
}

func (f *fakeRPC) Call(_ context.Context, method string, params map[string]any) (json.RawMessage, error) {
	f.mu.Lock()
	f.calls = append(f.calls, rpcCall{method, params})
	answer := f.answer
	f.mu.Unlock()
	return answer(method, params)
}

func (f *fakeRPC) setAnswer(a func(string, map[string]any) (json.RawMessage, error)) {
	f.mu.Lock()
	f.answer = a
	f.mu.Unlock()
}

func (f *fakeRPC) callsOf(method string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, c := range f.calls {
		if c.method == method {
			out = append(out, c.params)
		}
	}
	return out
}

type fakeForwards struct {
	fwds   []usecase.HarnessForward
	died   chan usecase.HarnessForward
	closed bool
}

func (f *fakeForwards) Forwards() []usecase.HarnessForward  { return f.fwds }
func (f *fakeForwards) Died() <-chan usecase.HarnessForward { return f.died }
func (f *fakeForwards) Close()                              { f.closed = true }

type fakeForwarder struct {
	mu     sync.Mutex
	opened []*fakeForwards
}

func (f *fakeForwarder) Open(_ context.Context, ports []usecase.HarnessPort) (Forwards, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fw := &fakeForwards{died: make(chan usecase.HarnessForward, len(ports))}
	for i, p := range ports {
		fw.fwds = append(fw.fwds, usecase.HarnessForward{Name: p.Name, LocalPort: 52000 + i, HostPort: p.HostPort})
	}
	f.opened = append(f.opened, fw)
	return fw, nil
}

func runJSON(state string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"type":"harness_run","run":{"id":"r1","session":"s1","scenario":"refund","sha":"a1b2c3d4e5f6","source":"file","state":%q,
"ports":[{"name":"web","service":"app","host_port":41873}],"services":["app","db"],
"steps":[{"n":1,"do":"Open {{web}}/orders/42","expect":"Refunded"}],"checks":[]}}`, state))
}

// newTestServer builds a console on a real loopback listener so the Host and
// Origin checks see a real port; requests go straight to the handler.
func newTestServer(t *testing.T, rpc RPC, fw Forwarder, log io.Writer) (*Server, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	if log == nil {
		log = io.Discard
	}
	s, err := New(Options{RPC: rpc, Forwarder: fw, Session: "s1", Token: testToken, Log: log}, ln)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.closeForwards)
	return s, "127.0.0.1:" + strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}

func do(t *testing.T, s *Server, method, host, path, token, origin, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, "http://"+host+path, rd)
	r.Host = host
	if token != "" {
		r.Header.Set(TokenHeader, token)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func readyRPC() *fakeRPC {
	return &fakeRPC{answer: func(method string, _ map[string]any) (json.RawMessage, error) {
		switch method {
		case "harness.status":
			return runJSON("ready"), nil
		case "harness.verdict":
			return json.RawMessage(`{"type":"harness_verdict","verdict":{"run":"r1","step":1,"result":"fail"},"todo":"t1"}`), nil
		case "harness.logs":
			return json.RawMessage(`{"type":"harness_logs","run":"r1","service":"db","lines":["ready"],"redacted":0}`), nil
		}
		return json.RawMessage(`{}`), nil
	}}
}

func TestTokenRequired(t *testing.T) {
	s, host := newTestServer(t, readyRPC(), &fakeForwarder{}, nil)
	for _, tc := range []struct {
		name, token string
		want        int
	}{
		{"none", "", http.StatusUnauthorized},
		{"wrong", "nope", http.StatusUnauthorized},
		{"prefix", testToken[:10], http.StatusUnauthorized},
		{"right", testToken, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := do(t, s, "GET", host, "/api/state", tc.token, "", ""); w.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.want, w.Body)
			}
		})
	}
	// Every API path checks the token, writes included.
	for _, p := range []string{"/api/list", "/api/logs?service=db", "/api/verdict", "/api/up", "/api/down", "/api/check"} {
		method := "POST"
		if strings.Contains(p, "list") || strings.Contains(p, "logs") {
			method = "GET"
		}
		if w := do(t, s, method, host, p, "", "http://"+host, `{}`); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without token: %d", method, p, w.Code)
		}
	}
	if s.URL() != "http://"+host+"/#token="+testToken {
		t.Errorf("URL %s", s.URL())
	}
}

func TestHostHeader(t *testing.T) {
	s, host := newTestServer(t, readyRPC(), &fakeForwarder{}, nil)
	_, port, _ := net.SplitHostPort(host)
	for _, tc := range []struct {
		host string
		want int
	}{
		{"127.0.0.1:" + port, http.StatusOK},
		{"localhost:" + port, http.StatusOK},
		{"evil.example:" + port, http.StatusForbidden},
		{"127.0.0.1:1", http.StatusForbidden},
		{"127.0.0.1", http.StatusForbidden},
		{"[::1]:" + port, http.StatusForbidden},
	} {
		if w := do(t, s, "GET", tc.host, "/api/state", testToken, "", ""); w.Code != tc.want {
			t.Errorf("Host %s: %d, want %d", tc.host, w.Code, tc.want)
		}
		if w := do(t, s, "GET", tc.host, "/", "", "", ""); w.Code != tc.want {
			t.Errorf("page with Host %s: %d, want %d", tc.host, w.Code, tc.want)
		}
	}
}

func TestOriginCheck(t *testing.T) {
	rpc := readyRPC()
	s, host := newTestServer(t, rpc, &fakeForwarder{}, nil)
	s.refresh(context.Background())
	body := `{"run":"r1","step":1,"result":"pass"}`
	for _, origin := range []string{"", "http://evil.example", "http://127.0.0.1:1", "null", "https://" + host} {
		if w := do(t, s, "POST", host, "/api/verdict", testToken, origin, body); w.Code != http.StatusForbidden {
			t.Errorf("Origin %q: %d, want 403", origin, w.Code)
		}
	}
	if n := len(rpc.callsOf("harness.verdict")); n != 0 {
		t.Fatalf("refused requests reached serve %d times", n)
	}
	for _, origin := range []string{"http://" + host, "http://localhost:" + strings.Split(host, ":")[1]} {
		if w := do(t, s, "POST", host, "/api/verdict", testToken, origin, body); w.Code != http.StatusOK {
			t.Errorf("Origin %q: %d %s", origin, w.Code, w.Body)
		}
	}
	// A POST that is not JSON is refused.
	r := httptest.NewRequest("POST", "http://"+host+"/api/verdict", strings.NewReader("run=r1"))
	r.Host = host
	r.Header.Set(TokenHeader, testToken)
	r.Header.Set("Origin", "http://"+host)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("form post: %d", w.Code)
	}
}

func TestVerdictFlow(t *testing.T) {
	rpc := readyRPC()
	fw := &fakeForwarder{}
	s, host := newTestServer(t, rpc, fw, nil)
	s.refresh(context.Background())

	w := do(t, s, "GET", host, "/api/state", testToken, "", "")
	var st struct {
		Run      struct{ ID, State string }
		Forwards []struct {
			Name     string `json:"name"`
			URL      string `json:"url"`
			HostPort int    `json:"host_port"`
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Run.ID != "r1" || st.Run.State != "ready" || len(st.Forwards) != 1 ||
		st.Forwards[0].URL != "http://127.0.0.1:52000" || st.Forwards[0].HostPort != 41873 {
		t.Fatalf("state %s", w.Body)
	}
	if got := rpc.callsOf("harness.status"); len(got) == 0 || got[0]["session"] != "s1" {
		t.Fatalf("status params %v", got)
	}

	origin := "http://" + host
	w = do(t, s, "POST", host, "/api/verdict", testToken, origin, `{"run":"r1","step":1,"result":"fail","note":"stays paid"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"todo":"t1"`) {
		t.Fatalf("verdict %d %s", w.Code, w.Body)
	}
	v := rpc.callsOf("harness.verdict")
	if len(v) != 1 || v[0]["run"] != "r1" || v[0]["step"] != 1 || v[0]["result"] != "fail" || v[0]["note"] != "stays paid" {
		t.Fatalf("verdict params %v", v)
	}

	// The whole scenario is step 0, sent without a step.
	w = do(t, s, "POST", host, "/api/verdict", testToken, origin, `{"run":"r1","step":0,"result":"pass"}`)
	if v = rpc.callsOf("harness.verdict"); w.Code != http.StatusOK || len(v) != 2 || v[1]["step"] != nil || v[1]["note"] != nil {
		t.Fatalf("scenario verdict %d %v", w.Code, v)
	}

	// Bad input never reaches serve; a stale run id is refused.
	for _, body := range []string{`{"run":"r1","step":1,"result":"maybe"}`, `{"run":"r1","step":-1,"result":"pass"}`, `{"run":"r1"`} {
		if w = do(t, s, "POST", host, "/api/verdict", testToken, origin, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", body, w.Code)
		}
	}
	if w = do(t, s, "POST", host, "/api/verdict", testToken, origin, `{"run":"r0","step":1,"result":"pass"}`); w.Code != http.StatusConflict {
		t.Errorf("stale run: %d %s", w.Code, w.Body)
	}
	if n := len(rpc.callsOf("harness.verdict")); n != 2 {
		t.Fatalf("serve got %d verdicts", n)
	}

	// A refusal from serve is shown with its code.
	rpc.setAnswer(func(string, map[string]any) (json.RawMessage, error) {
		return nil, &usecase.RPCError{Code: "needs_operator", Message: "only the operator records a verdict"}
	})
	w = do(t, s, "POST", host, "/api/verdict", testToken, origin, `{"run":"r1","step":1,"result":"pass"}`)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), `"code":"needs_operator"`) ||
		!strings.Contains(w.Body.String(), "only the operator") {
		t.Fatalf("refusal %d %s", w.Code, w.Body)
	}
}

func TestForwardsFollowRun(t *testing.T) {
	state := "ready"
	var mu sync.Mutex
	rpc := &fakeRPC{answer: func(string, map[string]any) (json.RawMessage, error) {
		mu.Lock()
		defer mu.Unlock()
		return runJSON(state), nil
	}}
	fw := &fakeForwarder{}
	s, _ := newTestServer(t, rpc, fw, nil)
	ctx := context.Background()
	s.refresh(ctx)
	s.refresh(ctx)
	if len(fw.opened) != 1 {
		t.Fatalf("opened %d forward sets for one ready run", len(fw.opened))
	}
	fw.opened[0].died <- usecase.HarnessForward{Name: "web"}
	s.refresh(ctx)
	if len(fw.opened) != 2 || !fw.opened[0].closed {
		t.Fatalf("a dead forward was not reopened: %d", len(fw.opened))
	}
	mu.Lock()
	state = "stopped"
	mu.Unlock()
	s.refresh(ctx)
	if !fw.opened[1].closed {
		t.Fatal("forwards stayed open after the run stopped")
	}
}

func TestLogsProxy(t *testing.T) {
	rpc := readyRPC()
	s, host := newTestServer(t, rpc, &fakeForwarder{}, nil)
	s.refresh(context.Background())
	w := do(t, s, "GET", host, "/api/logs?run=r1&service=db&lines=50", testToken, "", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"lines":["ready"]`) {
		t.Fatalf("logs %d %s", w.Code, w.Body)
	}
	got := rpc.callsOf("harness.logs")
	if len(got) != 1 || got[0]["run"] != "r1" || got[0]["service"] != "db" || got[0]["lines"] != 50 {
		t.Fatalf("logs params %v", got)
	}
	do(t, s, "GET", host, "/api/logs?run=r1&service=db&lines=99999", testToken, "", "")
	if got = rpc.callsOf("harness.logs"); got[1]["lines"] != MaxLogLines {
		t.Fatalf("lines not capped: %v", got[1])
	}
	for _, q := range []string{"?run=r1", "?run=r1&service=db&lines=x", "?run=r1&service=db&lines=0"} {
		if w = do(t, s, "GET", host, "/api/logs"+q, testToken, "", ""); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, w.Code)
		}
	}
	if w = do(t, s, "GET", host, "/api/logs?run=other&service=db", testToken, "", ""); w.Code != http.StatusConflict {
		t.Errorf("other run: %d", w.Code)
	}
}

func TestRunActions(t *testing.T) {
	rpc := readyRPC()
	s, host := newTestServer(t, rpc, &fakeForwarder{}, nil)
	s.refresh(context.Background())
	origin := "http://" + host
	if w := do(t, s, "POST", host, "/api/check", testToken, origin, `{"run":"r1","name":"unit"}`); w.Code != http.StatusOK {
		t.Fatalf("check %d %s", w.Code, w.Body)
	}
	if c := rpc.callsOf("harness.check"); len(c) != 1 || c[0]["run"] != "r1" || c[0]["name"] != "unit" {
		t.Fatalf("check params %v", c)
	}
	if w := do(t, s, "POST", host, "/api/up", testToken, origin, `{"scenario":"refund","dirty":true}`); w.Code != http.StatusOK {
		t.Fatalf("up %d %s", w.Code, w.Body)
	}
	if c := rpc.callsOf("harness.up"); len(c) != 1 || c[0]["session"] != "s1" || c[0]["scenario"] != "refund" || c[0]["dirty"] != true || c[0]["force"] != nil {
		t.Fatalf("up params %v", c)
	}
	if w := do(t, s, "POST", host, "/api/up", testToken, origin, `{"scenario":""}`); w.Code != http.StatusBadRequest {
		t.Errorf("empty scenario: %d", w.Code)
	}
	if w := do(t, s, "POST", host, "/api/down", testToken, origin, `{"run":"r1"}`); w.Code != http.StatusOK {
		t.Fatalf("down %d %s", w.Code, w.Body)
	}
	if c := rpc.callsOf("harness.down"); len(c) != 1 || c[0]["run"] != "r1" {
		t.Fatalf("down params %v", c)
	}
	if w := do(t, s, "GET", host, "/api/list", testToken, "", ""); w.Code != http.StatusOK {
		t.Fatalf("list %d", w.Code)
	}
	if c := rpc.callsOf("harness.list"); len(c) != 1 || c[0]["session"] != "s1" {
		t.Fatalf("list params %v", c)
	}
	if w := do(t, s, "GET", host, "/api/verdict", testToken, "", ""); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET on a write: %d", w.Code)
	}
}

func TestPageHeaders(t *testing.T) {
	s, host := newTestServer(t, readyRPC(), &fakeForwarder{}, nil)
	for _, p := range []string{"/", "/app.js", "/app.css"} {
		w := do(t, s, "GET", host, p, "", "", "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d", p, w.Code)
		}
		csp := w.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("%s CSP %q", p, csp)
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s sends CORS", p)
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s headers %v", p, w.Header())
		}
	}
	page := do(t, s, "GET", host, "/", "", "", "").Body.String()
	for _, banned := range []string{"http://", "https://", "<script>", "style="} {
		if strings.Contains(page, banned) {
			t.Errorf("page holds %q: assets must be embedded, scripts and styles external", banned)
		}
	}
	if w := do(t, s, "GET", host, "/nope", "", "", ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown path %d", w.Code)
	}
}

func TestTokenNeverLogged(t *testing.T) {
	var log syncBuffer
	rpc := readyRPC()
	s, host := newTestServer(t, rpc, &fakeForwarder{}, &log)
	s.refresh(context.Background())
	origin := "http://" + host
	do(t, s, "GET", host, "/api/state", testToken, "", "")
	do(t, s, "GET", host, "/api/state", testToken+"x", "", "")
	do(t, s, "GET", "evil:1", "/api/state", testToken, "", "")
	do(t, s, "POST", host, "/api/verdict", testToken, "http://evil", `{"run":"r1","step":1,"result":"pass"}`)
	do(t, s, "POST", host, "/api/verdict", testToken, origin, `{"run":"r1","step":1,"result":"pass"}`)
	do(t, s, "GET", host, "/api/logs?run=r1&service=db&token="+testToken, testToken, "", "")
	rpc.setAnswer(func(string, map[string]any) (json.RawMessage, error) { return nil, errors.New("ssh: broken") })
	do(t, s, "GET", host, "/api/list", testToken, "", "")
	s.refresh(context.Background())
	out := log.String()
	if out == "" {
		t.Fatal("nothing logged; the test would prove nothing")
	}
	if strings.Contains(out, testToken) || strings.Contains(out, testToken[:16]) {
		t.Fatalf("token in log:\n%s", out)
	}
}

func TestNewTokenIsRandom(t *testing.T) {
	ln1, _ := net.Listen("tcp", "127.0.0.1:0")
	ln2, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln1.Close()
	defer ln2.Close()
	a, err := New(Options{RPC: readyRPC(), Forwarder: &fakeForwarder{}, Session: "s1"}, ln1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := New(Options{RPC: readyRPC(), Forwarder: &fakeForwarder{}, Session: "s1"}, ln2)
	if a.token == b.token || len(a.token) < 43 {
		t.Fatalf("tokens %q %q", a.token, b.token)
	}
	if _, err := New(Options{RPC: readyRPC(), Forwarder: &fakeForwarder{}}, ln1); err == nil {
		t.Fatal("a console with neither session nor run was accepted")
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
