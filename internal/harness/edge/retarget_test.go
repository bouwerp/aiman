package edge

import (
	"context"
	"strings"
	"testing"
)

// TestDAPRetarget moves the relay between two debuggers, as serve does
// between the app and a debug check: the holder is closed, the next client
// reaches the new target, and an empty target admits nobody.
func TestDAPRetarget(t *testing.T) {
	e := newTestEdge(t)
	app, check := newFakeDAPServer(t), newFakeDAPServer(t)
	st, err := e.Add(RelaySpec{Name: "dap", Target: app.addr, Kind: KindDAP})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	first := dialDAP(t, st.Listen)
	if resp, err := first.request(1, "initialize", initArgs("aiman console")); err != nil || resp["success"] != true {
		t.Fatalf("initialize: %v %v", resp, err)
	}
	waitHolder(t, e, "dap", "aiman console")

	dropped, err := e.SetDAPTarget("dap", check.addr)
	if err != nil || dropped == nil || dropped.ClientName != "aiman console" {
		t.Fatalf("SetDAPTarget = %+v, %v", dropped, err)
	}
	if _, err := first.request(2, "threads", nil); err == nil {
		t.Fatal("the app's client stayed connected after the switch")
	}
	next := dialDAP(t, st.Listen)
	if resp, err := next.request(1, "initialize", initArgs("aiman console")); err != nil || resp["success"] != true {
		t.Fatalf("initialize after the switch: %v %v", resp, err)
	}
	if check.conns.Load() != 1 || app.conns.Load() != 1 {
		t.Fatalf("connections app %d check %d, want 1 and 1", app.conns.Load(), check.conns.Load())
	}
	if got := e.List()[0].Target; got != check.addr {
		t.Fatalf("status target %q, want %q", got, check.addr)
	}

	if _, err := e.SetDAPTarget("dap", ""); err != nil {
		t.Fatal(err)
	}
	none := dialDAP(t, st.Listen)
	resp, err := none.request(1, "initialize", initArgs("vscode"))
	if err != nil || resp["success"] != false || !strings.Contains(resp["message"].(string), "nothing to debug") {
		t.Fatalf("initialize with no target: %v %v", resp, err)
	}
	if _, err := e.SetDAPTarget("dap", "not an address"); err == nil {
		t.Fatal("a bad target was accepted")
	}
	if _, err := e.SetDAPTarget("missing", check.addr); err == nil {
		t.Fatal("an unknown relay was accepted")
	}
	plain, err := e.Add(RelaySpec{Name: "web", Target: app.addr})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.SetDAPTarget(plain.Name, check.addr); err == nil {
		t.Fatal("a plain relay was retargeted")
	}
	if _, err := e.Add(RelaySpec{Name: "web2"}); err == nil {
		t.Fatal("a plain relay without a target was accepted")
	}
	if _, err := e.Add(RelaySpec{Name: "dap2", Kind: KindDAP}); err != nil {
		t.Fatalf("a debugger relay without a target: %v", err)
	}
}

// TestControlDAPTarget switches the target over the control socket.
func TestControlDAPTarget(t *testing.T) {
	e := newTestEdge(t)
	srv := newFakeDAPServer(t)
	if _, err := e.Add(RelaySpec{Name: "dap", Kind: KindDAP}); err != nil {
		t.Fatal(err)
	}
	cl := Client{Socket: startControl(t, e), Token: testToken}
	if _, err := cl.DAPTarget(context.Background(), "dap", srv.addr); err != nil {
		t.Fatalf("dap_target: %v", err)
	}
	if _, err := cl.DAPTarget(context.Background(), "dap", "nowhere"); err == nil {
		t.Fatal("a bad target went through the control socket")
	}
	if got := e.List()[0].Target; got != srv.addr {
		t.Fatalf("target %q", got)
	}
}
