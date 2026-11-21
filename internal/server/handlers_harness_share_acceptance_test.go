package server

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
)

const shareBase = "https://aiman-gateway-h.tail1.ts.net"

var shareURLRe = regexp.MustCompile(`^` + regexp.QuoteMeta(shareBase) + `:8090/s/([A-Za-z0-9_-]{43})$`)

type shareResult struct {
	Type  string `json:"type"`
	URL   string `json:"url"`
	Share string `json:"share"`
	Run   string `json:"run"`
}

type shareState struct {
	Type    string `json:"type"`
	Share   string `json:"share"`
	Session string `json:"session"`
	Run     string `json:"run"`
	State   string `json:"state"`
}

func announceGateway(t *testing.T, srv *Server) {
	t.Helper()
	var res struct {
		Type string `json:"type"`
	}
	rpcOK(t, srv, "", "gateway.announce", map[string]any{
		"base": shareBase, "console_port": 8090, "scheme": "https", "pid": os.Getpid(),
	}, &res)
	if res.Type != "gateway_announced" {
		t.Fatalf("announce type = %q", res.Type)
	}
}

func readyRun(t *testing.T, srv *Server) harness.Run {
	t.Helper()
	run := harnessUp(t, srv, "", map[string]any{"session": "s1", "scenario": "plain", "wait": true})
	if run.State != harness.StateReady {
		t.Fatalf("run = %+v", run)
	}
	return run
}

func shareOf(t *testing.T, srv *Server, caller string) shareResult {
	t.Helper()
	var res shareResult
	rpcOK(t, srv, caller, "harness.share", map[string]any{"session": "s1"}, &res)
	if res.Type != "harness_share" {
		t.Fatalf("share type = %q", res.Type)
	}
	m := shareURLRe.FindStringSubmatch(res.URL)
	if m == nil {
		t.Fatalf("share url = %q", res.URL)
	}
	if m[1] != res.Share {
		t.Fatalf("url id %q, share %q", m[1], res.Share)
	}
	return res
}

func shareGet(t *testing.T, srv *Server, id string) shareState {
	t.Helper()
	var st shareState
	rpcOK(t, srv, "", "harness.share_get", map[string]any{"id": id}, &st)
	if st.Type != "harness_share_state" || st.Share != id {
		t.Fatalf("share_get = %+v", st)
	}
	return st
}

func waitShareEvent(t *testing.T, ch <-chan SessionEvent, id, state string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == "harness_share" && ev.ID == id && ev.State == state {
				return
			}
		case <-deadline:
			t.Fatalf("no harness_share event {%s, %s}", id, state)
		}
	}
}

func TestHarnessShareNeedsAnnounce(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{})
	readyRun(t, srv)
	resp := rpc(t, srv, "s1", "harness.share", map[string]any{"session": "s1"})
	wantHarnessCode(t, "share without gateway", resp, CodeUnavailable)
	if !strings.Contains(resp.Error.Message, "gateway is not running") {
		t.Fatalf("message = %q", resp.Error.Message)
	}
}

func TestHarnessShareStaleAnnounce(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{})
	readyRun(t, srv)
	repo := srv.repo.(*sqlite.Repository)
	if err := repo.SaveGatewayAnnounce(context.Background(), domain.GatewayAnnounce{
		Base: shareBase, ConsolePort: 8090, Scheme: "https", PID: os.Getpid(), At: time.Now().Add(-5 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	resp := rpc(t, srv, "s1", "harness.share", map[string]any{"session": "s1"})
	wantHarnessCode(t, "share with stale announce", resp, CodeUnavailable)
	if !strings.Contains(resp.Error.Message, "gateway is not running") {
		t.Fatalf("message = %q", resp.Error.Message)
	}
}

func TestHarnessShareLifecycle(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{})
	announceGateway(t, srv)
	run := readyRun(t, srv)
	ch, cancel := srv.hub.Subscribe()
	defer cancel()

	// A session caller shares its own run.
	first := shareOf(t, srv, "s1")
	if first.Run != run.ID {
		t.Fatalf("share run = %q, want %q", first.Run, run.ID)
	}
	waitShareEvent(t, ch, first.Share, "live")
	st := shareGet(t, srv, first.Share)
	if st.Session != "s1" || st.Run != run.ID || st.State != "live" {
		t.Fatalf("share_get = %+v", st)
	}
	rows := ledgerRows(t, srv, domain.EventHarnessShared)
	if len(rows) != 1 || rows[0]["session"] != "s1" || rows[0]["run"] != run.ID || rows[0]["share"] != first.Share {
		t.Fatalf("harness.shared = %v", rows)
	}

	// A second share revokes the first.
	second := shareOf(t, srv, "")
	if second.Share == first.Share {
		t.Fatal("second share reused the id")
	}
	if st := shareGet(t, srv, first.Share); st.State != "ended" {
		t.Fatalf("first share after second = %+v", st)
	}
	if st := shareGet(t, srv, second.Share); st.State != "live" {
		t.Fatalf("second share = %+v", st)
	}
	if rows := ledgerRows(t, srv, domain.EventHarnessShared); len(rows) != 2 {
		t.Fatalf("harness.shared rows = %v", rows)
	}

	// Unshare revokes the live one.
	var un struct {
		Type    string `json:"type"`
		Revoked int    `json:"revoked"`
	}
	rpcOK(t, srv, "s1", "harness.unshare", map[string]any{"session": "s1"}, &un)
	if un.Type != "harness_unshared" || un.Revoked != 1 {
		t.Fatalf("unshare = %+v", un)
	}
	waitShareEvent(t, ch, second.Share, "ended")
	if st := shareGet(t, srv, second.Share); st.State != "ended" {
		t.Fatalf("second share after unshare = %+v", st)
	}
}

func TestHarnessShareEndsWithRun(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{})
	announceGateway(t, srv)
	run := readyRun(t, srv)
	sh := shareOf(t, srv, "s1")
	rpcOK(t, srv, "", "harness.down", map[string]any{"run": run.ID}, nil)
	if st := shareGet(t, srv, sh.Share); st.State != "ended" {
		t.Fatalf("share after down = %+v", st)
	}
}

func TestHarnessShareNotReady(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{})
	announceGateway(t, srv)
	// No run at all.
	wantHarnessCode(t, "share with no run", rpc(t, srv, "s1", "harness.share", map[string]any{"session": "s1"}), harness.CodeNotRunning)
	// A stopped run.
	run := readyRun(t, srv)
	rpcOK(t, srv, "", "harness.down", map[string]any{"run": run.ID}, nil)
	wantHarnessCode(t, "share a stopped run", rpc(t, srv, "s1", "harness.share", map[string]any{"session": "s1"}), harness.CodeNotRunning)
	if rows := ledgerRows(t, srv, domain.EventHarnessShared); len(rows) != 0 {
		t.Fatalf("a refused share was recorded: %v", rows)
	}
}

func TestHarnessShareScope(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{})
	announceGateway(t, srv)
	readyRun(t, srv)
	wantHarnessCode(t, "s2 shares s1", rpc(t, srv, "s2", "harness.share", map[string]any{"session": "s1"}), CodeSessionScope)
}

func TestHarnessShareGetNeedsOperator(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{})
	announceGateway(t, srv)
	readyRun(t, srv)
	sh := shareOf(t, srv, "s1")
	wantHarnessCode(t, "session share_get", rpc(t, srv, "s1", "harness.share_get", map[string]any{"id": sh.Share}), CodeNeedsOperator)
	wantHarnessCode(t, "unknown share", rpc(t, srv, "", "harness.share_get", map[string]any{"id": strings.Repeat("x", 43)}), CodeNotFound)
}

func TestHarnessShareAnnounceNeedsOperator(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{})
	wantHarnessCode(t, "session announce", rpc(t, srv, "s1", "gateway.announce", map[string]any{
		"base": shareBase, "console_port": 8090, "scheme": "https", "pid": os.Getpid(),
	}), CodeNeedsOperator)
	readyRun(t, srv)
	// The refused announce left no gateway behind.
	wantHarnessCode(t, "share after refused announce", rpc(t, srv, "s1", "harness.share", map[string]any{"session": "s1"}), CodeUnavailable)
}

func TestHarnessShareVerdictTester(t *testing.T) {
	fx := harnessFixtureOf(t, harnessSetup{})
	srv := fx.srv
	run := readyRun(t, srv)
	rpcOK(t, srv, "", "harness.verdict", map[string]any{
		"run": run.ID, "step": 1, "result": "fail", "note": "refund button does nothing", "tester": "alice@example.com",
	}, nil)
	events, err := srv.repo.(sessionEventLister).ListSessionEvents(context.Background(), "s1", 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	var actors []domain.EventActor
	for _, ev := range events {
		if ev.Kind == domain.EventHarnessVerdict {
			actors = append(actors, ev.Actor)
		}
	}
	if len(actors) != 1 || actors[0] != "tester:alice@example.com" {
		t.Fatalf("verdict actors = %v", actors)
	}
	want := "[aiman] tester alice@example.com marked harness scenario"
	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, r := range fx.notes.records() {
			if r.SessionID == "s1" && strings.Contains(r.Text, want) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no steer note containing %q in %+v", want, fx.notes.records())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
