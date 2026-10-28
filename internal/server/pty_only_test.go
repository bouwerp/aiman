package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/ptyruntime/ptyfake"
)

// ptyServer is a serve instance over a fake PTY runtime and a fake remote,
// with the given rows stored.
func ptyServer(t *testing.T, sessions ...domain.Session) (*Server, *ptyfake.FakePTY, *fakeRemote) {
	t.Helper()
	repo := testRepo(t)
	for i := range sessions {
		if sessions[i].Status == "" {
			sessions[i].Status = domain.SessionStatusActive
		}
		if sessions[i].CreatedAt.IsZero() {
			sessions[i].CreatedAt = time.Now()
		}
		if err := repo.Save(context.Background(), &sessions[i]); err != nil {
			t.Fatal(err)
		}
	}
	pty := ptyfake.New()
	remote := &fakeRemote{}
	return New(nil, repo, remote, nil, nil, pty, "test"), pty, remote
}

var _ PTYRuntime = (*ptyfake.FakePTY)(nil)

func legacyRow() domain.Session {
	const id = "old"
	return domain.Session{
		ID: id, Name: id, Group: "g", TmuxSession: "old-" + id, RemoteHost: "box",
		Backend: domain.BackendLegacyTmux,
	}
}

func TestNewDropsTypedNilRuntime(t *testing.T) {
	var mgr *ptyruntime.Manager
	if srv := New(nil, nil, nil, nil, nil, mgr, "test"); srv.pty != nil {
		t.Fatalf("pty = %#v, want nil", srv.pty)
	}
}

func TestSessionListReportsLegacyTmuxRowsInactive(t *testing.T) {
	srv, pty, _ := ptyServer(t, legacyRow(), domain.Session{ID: "new", Name: "new", Group: "g", Backend: domain.BackendPTY})
	pty.Add(ptyruntime.SessionInfo{ID: "new"})
	var out struct {
		Sessions []SessionInfo `json:"sessions"`
	}
	rpcOK(t, srv, "", "session.list", map[string]any{}, &out)
	got := map[string]string{}
	for _, s := range out.Sessions {
		got[s.ID] = s.Status
	}
	if got["old"] != string(domain.SessionStatusInactive) {
		t.Fatalf("legacy row status = %q, want inactive (%+v)", got["old"], out.Sessions)
	}
	if got["new"] != string(domain.SessionStatusActive) {
		t.Fatalf("pty row status = %q, want active", got["new"])
	}
}

func TestLiveSessionsUsesInProcessRuntime(t *testing.T) {
	srv, pty, remote := ptyServer(t)
	pty.Add(ptyruntime.SessionInfo{ID: "s1", Name: "term-s1", Dir: "/w/s1"})
	pty.Add(ptyruntime.SessionInfo{ID: "gone", Status: ptyruntime.StatusExited})
	live := srv.liveSessions(context.Background())
	if len(live) != 1 {
		t.Fatalf("live = %+v, want only the running session", live)
	}
	l := live[0]
	if l.ID != "s1" || l.TmuxSession != "term-s1" || l.WorkingDirectory != "/w/s1" || l.Backend != domain.BackendPTY || l.Status != domain.SessionStatusActive {
		t.Fatalf("live = %+v", l)
	}
	if len(remote.execs) != 0 {
		t.Fatalf("liveSessions ran remote commands: %v", remote.execs)
	}
}

func TestClassifyPTYWithEmptyName(t *testing.T) {
	srv, pty, _ := ptyServer(t, domain.Session{ID: "s1", Name: "s1", Group: "g", Backend: domain.BackendPTY})
	pty.Add(ptyruntime.SessionInfo{ID: "s1"})
	pty.SetScreen("s1", "✻ Reticulating splines… (12s · esc to interrupt)")
	st, _ := srv.classify(context.Background(), domain.Session{ID: "s1", Backend: domain.BackendPTY})
	if st != domain.AgentStateWorking {
		t.Fatalf("state = %q, want working", st)
	}
}

func TestCreateIgnoresBackendParam(t *testing.T) {
	srv, _, creator := pickServer(t, nil)
	resp := rpc(t, srv, "", "session.create", map[string]any{"quick": true, "agent": "claude", "backend": "tmux"})
	if resp.Error != nil {
		t.Fatalf("create: %+v", resp.Error)
	}
	if creator.cfg.Agent == nil || creator.cfg.Agent.Command != "claude" {
		t.Fatalf("create with a backend param should still launch the agent: %+v", creator.cfg.Agent)
	}
}

func TestSteerToLegacyRowIsHeld(t *testing.T) {
	row := legacyRow()
	row.HookState = domain.AgentStateIdle
	row.HookStateAt = time.Now().UTC()
	row.HookStateSource = agenthook.SourceIdlePrompt
	srv, pty, remote := ptyServer(t, row)
	ok, reason := srv.steerReady(context.Background(), "old", time.Time{})
	if ok || reason != steerHeldLegacy {
		t.Fatalf("steerReady = %v %q, want held %q", ok, reason, steerHeldLegacy)
	}
	if _, err := srv.deliverSteer(context.Background(), "old", "go"); err == nil {
		t.Fatal("deliverSteer to a legacy row succeeded")
	}
	if len(pty.Writes()) != 0 || len(remote.execs) != 0 {
		t.Fatalf("typed into a legacy row: writes %v execs %v", pty.Writes(), remote.execs)
	}
}

func TestPromptLegacyRowReturnsLegacyTerminal(t *testing.T) {
	srv, pty, remote := ptyServer(t, legacyRow())
	resp := rpc(t, srv, "", "session.prompt", map[string]any{"id": "old", "text": "hi", "force": true})
	if resp.Error == nil || resp.Error.Code != CodeLegacyTerminal {
		t.Fatalf("error = %+v, want %s", resp.Error, CodeLegacyTerminal)
	}
	if !strings.Contains(resp.Error.Message, "tmux kill-session -t '=old-old'") || !strings.Contains(resp.Error.Message, "box") {
		t.Fatalf("message %q must carry the stop hint", resp.Error.Message)
	}
	if len(pty.Writes()) != 0 || len(remote.execs) != 0 {
		t.Fatalf("prompt reached a terminal: writes %v execs %v", pty.Writes(), remote.execs)
	}
}

func TestVerifyTerminalIsPTYOnly(t *testing.T) {
	srv, pty, remote := ptyServer(t)
	pty.Add(ptyruntime.SessionInfo{ID: "r1"})
	term := serverTerminal{s: srv}
	ctx := context.Background()
	// A row without a recorded backend is a pty session.
	sess := domain.Session{ID: "r1", TmuxSession: "rev"}
	if !term.Alive(ctx, sess) {
		t.Fatal("running pty session reported dead")
	}
	if err := term.Stop(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if kills := pty.Kills(); len(kills) != 1 || kills[0] != "r1" {
		t.Fatalf("kills = %v, want [r1]", kills)
	}
	if term.Alive(ctx, sess) {
		t.Fatal("killed session reported alive")
	}
	legacy := legacyRow()
	if term.Alive(ctx, legacy) {
		t.Fatal("legacy row reported alive")
	}
	if err := term.Stop(ctx, legacy); err == nil {
		t.Fatal("Stop on a legacy row succeeded")
	}
	if len(remote.execs) != 0 {
		t.Fatalf("verify terminal ran remote commands: %v", remote.execs)
	}
}
