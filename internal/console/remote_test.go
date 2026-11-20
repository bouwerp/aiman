package console

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
)

func TestRemoteAppLinksUseFirstHost(t *testing.T) {
	s := remoteServer(t, readyRPC(), Options{Port: 8090, HTTPS: true})
	s.refresh(context.Background())
	w := do(t, s, "GET", "aiman-gateway-h:8090", "/api/state", testToken, "", "")
	var st struct {
		Forwards []forwardView `json:"forwards"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || len(st.Forwards) != 1 {
		t.Fatalf("state %s", w.Body)
	}
	if got := st.Forwards[0].URL; got != "http://aiman-gateway-h:41873" {
		t.Fatalf("app link %q", got)
	}
	if u := s.URL(); !strings.HasPrefix(u, "https://aiman-gateway-h:8090/#token=") {
		t.Fatalf("console URL %q", u)
	}
}

func TestRemoteIPv6Host(t *testing.T) {
	s := remoteServer(t, readyRPC(), Options{Port: 443, HTTPS: true, Hosts: []string{"fd7a:115c::5"}})
	for _, h := range []string{"[fd7a:115c::5]:443", "[fd7a:115c::5]"} {
		if w := do(t, s, "GET", h, "/api/state", testToken, "", ""); w.Code != 200 {
			t.Errorf("Host %s: %d", h, w.Code)
		}
	}
}

func TestRemoteRefusesListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if _, err := New(Options{RPC: readyRPC(), Forwarder: HostForwarder{}, Session: "s1", Hosts: []string{"h"}, Port: 8090}, ln); err == nil {
		t.Fatal("a shared console took a listener")
	}
	if _, err := New(Options{RPC: readyRPC(), Forwarder: HostForwarder{}, Session: "s1"}, nil); err == nil {
		t.Fatal("a loopback console with no listener was accepted")
	}
}
