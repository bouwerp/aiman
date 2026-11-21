package console

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/usecase"
)

var remoteHostNames = []string{"aiman-gateway-h", "aiman-gateway-h.tail1.ts.net", "100.64.0.5"}

// remoteServer is a console with no listener, served through Handler only.
func remoteServer(t *testing.T, rpc RPC, o Options) *Server {
	t.Helper()
	o.RPC = rpc
	if o.Forwarder == nil {
		o.Forwarder = HostForwarder{}
	}
	if o.Session == "" && o.Run == "" {
		o.Session = "s1"
	}
	if o.Token == "" {
		o.Token = testToken
	}
	if o.Hosts == nil {
		o.Hosts = remoteHostNames
	}
	s, err := New(o, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.closeForwards)
	return s
}

func TestRemoteHostsHostHeader(t *testing.T) {
	s := remoteServer(t, readyRPC(), Options{Port: 8090, HTTPS: true})
	for _, tc := range []struct {
		host string
		want int
	}{
		{"aiman-gateway-h:8090", http.StatusOK},
		{"aiman-gateway-h.tail1.ts.net:8090", http.StatusOK},
		{"100.64.0.5:8090", http.StatusOK},
		{"evil.example:8090", http.StatusForbidden},
		{"aiman-gateway-h:8091", http.StatusForbidden},
		{"aiman-gateway-h", http.StatusForbidden},
	} {
		if w := do(t, s, "GET", tc.host, "/api/state", testToken, "", ""); w.Code != tc.want {
			t.Errorf("Host %s: %d, want %d", tc.host, w.Code, tc.want)
		}
		if w := do(t, s, "GET", tc.host, "/", "", "", ""); w.Code != tc.want {
			t.Errorf("page with Host %s: %d, want %d", tc.host, w.Code, tc.want)
		}
	}
	// The token is still required.
	if w := do(t, s, "GET", "aiman-gateway-h:8090", "/api/state", "", "", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", w.Code)
	}
}

func TestRemoteHostsDefaultPort(t *testing.T) {
	https := remoteServer(t, readyRPC(), Options{Port: 443, HTTPS: true})
	for _, h := range []string{"aiman-gateway-h", "aiman-gateway-h:443"} {
		if w := do(t, https, "GET", h, "/api/state", testToken, "", ""); w.Code != http.StatusOK {
			t.Errorf("https Host %s: %d", h, w.Code)
		}
	}
	plain := remoteServer(t, readyRPC(), Options{Port: 80})
	for _, h := range []string{"aiman-gateway-h", "aiman-gateway-h:80"} {
		if w := do(t, plain, "GET", h, "/api/state", testToken, "", ""); w.Code != http.StatusOK {
			t.Errorf("http Host %s: %d", h, w.Code)
		}
	}
	// A bare name is the default port only: 443 under http is not it.
	odd := remoteServer(t, readyRPC(), Options{Port: 443})
	if w := do(t, odd, "GET", "aiman-gateway-h", "/api/state", testToken, "", ""); w.Code != http.StatusForbidden {
		t.Errorf("bare Host on http at 443: %d", w.Code)
	}
}

func TestRemoteHostsOrigin(t *testing.T) {
	rpc := readyRPC()
	s := remoteServer(t, rpc, Options{Port: 8090, HTTPS: true})
	s.refresh(context.Background())
	host := "aiman-gateway-h:8090"
	body := `{"run":"r1","step":1,"result":"pass"}`
	for _, origin := range []string{"", "http://aiman-gateway-h:8090", "https://evil.example:8090", "https://aiman-gateway-h:8091", "http://127.0.0.1:8090"} {
		if w := do(t, s, "POST", host, "/api/verdict", testToken, origin, body); w.Code != http.StatusForbidden {
			t.Errorf("Origin %q: %d, want 403", origin, w.Code)
		}
	}
	if n := len(rpc.callsOf("harness.verdict")); n != 0 {
		t.Fatalf("refused requests reached serve %d times", n)
	}
	for _, origin := range []string{"https://aiman-gateway-h:8090", "https://aiman-gateway-h.tail1.ts.net:8090", "https://100.64.0.5:8090"} {
		if w := do(t, s, "POST", host, "/api/verdict", testToken, origin, body); w.Code != http.StatusOK {
			t.Errorf("Origin %q: %d %s", origin, w.Code, w.Body)
		}
	}

	plain := remoteServer(t, readyRPC(), Options{Port: 8090})
	plain.refresh(context.Background())
	if w := do(t, plain, "POST", host, "/api/verdict", testToken, "http://aiman-gateway-h:8090", body); w.Code != http.StatusOK {
		t.Errorf("http origin on an http console: %d %s", w.Code, w.Body)
	}
	if w := do(t, plain, "POST", host, "/api/verdict", testToken, "https://aiman-gateway-h:8090", body); w.Code != http.StatusForbidden {
		t.Errorf("https origin on an http console: %d", w.Code)
	}
}

func TestRemoteHostsCSP(t *testing.T) {
	s := remoteServer(t, readyRPC(), Options{Port: 8090, HTTPS: true})
	csp := do(t, s, "GET", "aiman-gateway-h:8090", "/", "", "", "").Header().Get("Content-Security-Policy")
	for _, h := range remoteHostNames {
		if !strings.Contains(csp, "wss://"+h+":8090") {
			t.Errorf("CSP lacks wss://%s:8090: %s", h, csp)
		}
	}
	if strings.Contains(csp, "ws://") {
		t.Errorf("https console CSP names ws://: %s", csp)
	}
	plain := remoteServer(t, readyRPC(), Options{Port: 8090})
	csp = do(t, plain, "GET", "aiman-gateway-h:8090", "/", "", "", "").Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "ws://aiman-gateway-h:8090") || strings.Contains(csp, "wss://") {
		t.Errorf("http console CSP: %s", csp)
	}
}

func TestRemoteHostsStateShared(t *testing.T) {
	s := remoteServer(t, readyRPC(), Options{Port: 8090, HTTPS: true})
	s.refresh(context.Background())
	w := do(t, s, "GET", "aiman-gateway-h:8090", "/api/state", testToken, "", "")
	var st struct {
		Shared bool `json:"shared"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || !st.Shared {
		t.Fatalf("remote state %s", w.Body)
	}
	local, host := newTestServer(t, readyRPC(), &fakeForwarder{}, nil)
	w = do(t, local, "GET", host, "/api/state", testToken, "", "")
	st.Shared = false
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || st.Shared {
		t.Fatalf("loopback state %s", w.Body)
	}
}

func TestRemoteHostsLoopbackRuleKept(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("no IPv6 loopback")
	}
	defer func() { _ = ln.Close() }()
	if _, err := New(Options{RPC: readyRPC(), Forwarder: &fakeForwarder{}, Session: "s1"}, ln); err == nil {
		t.Fatal("a listener off 127.0.0.1 with no Hosts was accepted")
	}
}

func TestMethodSetRefusesOthers(t *testing.T) {
	rpc := readyRPC()
	s := remoteServer(t, rpc, Options{Port: 8090, HTTPS: true, Methods: []string{"harness.status", "harness.verdict", "harness.logs"}})
	s.refresh(context.Background())
	host, origin := "aiman-gateway-h:8090", "https://aiman-gateway-h:8090"

	w := do(t, s, "POST", host, "/api/up", testToken, origin, `{"scenario":"refund"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("up: %d %s", w.Code, w.Body)
	}
	var e struct {
		Error apiError `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil || e.Error.Code != "method_not_allowed" {
		t.Fatalf("up body %s", w.Body)
	}
	if n := len(rpc.callsOf("harness.up")); n != 0 {
		t.Fatalf("harness.up reached serve %d times", n)
	}
	if w := do(t, s, "POST", host, "/api/down", testToken, origin, `{"run":"r1"}`); w.Code != http.StatusForbidden {
		t.Fatalf("down: %d %s", w.Code, w.Body)
	}
	if n := len(rpc.callsOf("harness.down")); n != 0 {
		t.Fatalf("harness.down reached serve %d times", n)
	}
	if w := do(t, s, "GET", host, "/api/list", testToken, "", ""); w.Code != http.StatusForbidden {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	if n := len(rpc.callsOf("harness.list")); n != 0 {
		t.Fatalf("harness.list reached serve %d times", n)
	}

	// Methods in the set pass.
	if w := do(t, s, "POST", host, "/api/verdict", testToken, origin, `{"run":"r1","step":1,"result":"fail","note":"x"}`); w.Code != http.StatusOK {
		t.Fatalf("verdict: %d %s", w.Code, w.Body)
	}
	if n := len(rpc.callsOf("harness.verdict")); n != 1 {
		t.Fatalf("harness.verdict calls %d", n)
	}
	if w := do(t, s, "GET", host, "/api/logs?run=r1&service=db", testToken, "", ""); w.Code != http.StatusOK {
		t.Fatalf("logs: %d %s", w.Code, w.Body)
	}
}

func TestMethodSetNilAllowsAll(t *testing.T) {
	rpc := readyRPC()
	s := remoteServer(t, rpc, Options{Port: 8090, HTTPS: true})
	s.refresh(context.Background())
	if w := do(t, s, "GET", "aiman-gateway-h:8090", "/api/list", testToken, "", ""); w.Code == http.StatusForbidden {
		t.Fatalf("list with no method set: %d %s", w.Code, w.Body)
	}
	if n := len(rpc.callsOf("harness.list")); n != 1 {
		t.Fatalf("harness.list calls %d", n)
	}
}

func TestHostForwarderMapsPortsDirectly(t *testing.T) {
	var f Forwarder = HostForwarder{}
	fw, err := f.Open(context.Background(), []usecase.HarnessPort{{Name: "web", HostPort: 41873}, {Name: "dap", HostPort: 41874}})
	if err != nil {
		t.Fatal(err)
	}
	got := fw.Forwards()
	if len(got) != 2 {
		t.Fatalf("forwards %+v", got)
	}
	for i, want := range []struct {
		name string
		port int
	}{{"web", 41873}, {"dap", 41874}} {
		if got[i].Name != want.name || got[i].HostPort != want.port || got[i].LocalPort != want.port {
			t.Fatalf("forward %d = %+v", i, got[i])
		}
	}
	select {
	case d := <-fw.Died():
		t.Fatalf("a host forward died: %+v", d)
	case <-time.After(50 * time.Millisecond):
	}
	fw.Close()
	fw.Close()
}

func TestHostForwarderInConsoleState(t *testing.T) {
	s := remoteServer(t, readyRPC(), Options{Port: 8090, HTTPS: true})
	s.refresh(context.Background())
	w := do(t, s, "GET", "aiman-gateway-h:8090", "/api/state", testToken, "", "")
	var st struct {
		Forwards []struct {
			Name     string `json:"name"`
			HostPort int    `json:"host_port"`
		} `json:"forwards"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Forwards) != 1 || st.Forwards[0].Name != "web" || st.Forwards[0].HostPort != 41873 {
		t.Fatalf("state %s", w.Body)
	}
}
