package ui

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

func newTestModelWithSession(t *testing.T, s domain.Session) *Model {
	t.Helper()
	cfg := &config.Config{Remotes: []config.Remote{{Host: s.RemoteHost, User: "code"}}}
	model := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	return model
}

// An inactive session whose AgentName is already known (persisted, or
// inferred from a hook sidecar during discovery) resumes immediately on "s"
// — no agent picker, no confirmation dialog, and no blocking loading screen:
// the restart runs in the background like session creation.
func TestHandleSessionManageKey_KnownAgentResumesWithoutPicker(t *testing.T) {
	model := newTestModelWithSession(t, domain.Session{
		ID: "sess-1", RemoteHost: "devbox", TmuxSession: "PB-1", RepoName: "org/repo",
		Status: domain.SessionStatusInactive, AgentName: "Codex CLI",
	})

	newModel, cmd, handled := model.handleSessionManageKey(pressKey("s"))
	if !handled {
		t.Fatal("expected the key to be handled")
	}
	m := newModel.(*Model)
	if m.state != viewStateMain {
		t.Fatalf("state = %v, want viewStateMain (background restart, no loading screen)", m.state)
	}
	if _, ok := m.creatingSessions["sess-1"]; !ok {
		t.Fatal("expected a background-restart tracking entry for the session")
	}
	if m.sessionCfg.Agent == nil || m.sessionCfg.Agent.Name != "Codex CLI" {
		t.Fatalf("expected sessionCfg.Agent to resolve to Codex CLI, got %+v", m.sessionCfg.Agent)
	}
	if cmd == nil {
		t.Fatal("expected a restart command")
	}
}

// An unresolvable agent identity must fall back to the manual picker rather
// than guessing, or getting stuck.
func TestHandleSessionManageKey_UnknownAgentFallsBackToPicker(t *testing.T) {
	model := newTestModelWithSession(t, domain.Session{
		ID: "sess-1", RemoteHost: "devbox", TmuxSession: "PB-1", RepoName: "org/repo",
		Status: domain.SessionStatusInactive, AgentName: "Some Unheard Of CLI",
	})

	newModel, cmd, handled := model.handleSessionManageKey(pressKey("s"))
	if !handled {
		t.Fatal("expected the key to be handled")
	}
	m := newModel.(*Model)
	if m.loadingNext != viewStateRestartAgentPicker {
		t.Fatalf("loadingNext = %v, want viewStateRestartAgentPicker", m.loadingNext)
	}
	if m.sessionCfg.Agent != nil {
		t.Fatalf("expected no agent resolved, got %+v", m.sessionCfg.Agent)
	}
	if cmd == nil {
		t.Fatal("expected a fetch-agents command")
	}
}

// A live session still confirms before anything touches its pane, whether
// that leads to an auto-resume or the picker.
func TestHandleSessionManageKey_ActiveSessionAlwaysConfirmsFirst(t *testing.T) {
	model := newTestModelWithSession(t, domain.Session{
		ID: "sess-1", RemoteHost: "devbox", TmuxSession: "PB-1", RepoName: "org/repo",
		Status: domain.SessionStatusActive, AgentName: "Codex CLI",
	})

	newModel, _, handled := model.handleSessionManageKey(pressKey("s"))
	if !handled {
		t.Fatal("expected the key to be handled")
	}
	m := newModel.(*Model)
	if m.state != viewStateRestartConfirm {
		t.Fatalf("state = %v, want viewStateRestartConfirm", m.state)
	}
	if m.sessionCfg.Agent == nil || m.sessionCfg.Agent.Name != "Codex CLI" {
		t.Fatalf("expected the known agent to already be resolved for the confirm step, got %+v", m.sessionCfg.Agent)
	}
}

// "S" is the deliberate "switch agent" action: it must show the picker even
// when the agent is already known.
func TestHandleSessionManageKey_ForceSwitchAlwaysShowsPicker(t *testing.T) {
	model := newTestModelWithSession(t, domain.Session{
		ID: "sess-1", RemoteHost: "devbox", TmuxSession: "PB-1", RepoName: "org/repo",
		Status: domain.SessionStatusInactive, AgentName: "Codex CLI",
	})

	newModel, cmd, handled := model.handleSessionManageKey(pressKey("S"))
	if !handled {
		t.Fatal("expected the key to be handled")
	}
	m := newModel.(*Model)
	if m.loadingNext != viewStateRestartAgentPicker {
		t.Fatalf("loadingNext = %v, want viewStateRestartAgentPicker", m.loadingNext)
	}
	if m.sessionCfg.Agent != nil {
		t.Fatalf("expected the known agent to be ignored when switching, got %+v", m.sessionCfg.Agent)
	}
	if cmd == nil {
		t.Fatal("expected a fetch-agents command")
	}
}

// handleRestartConfirmUpdate must resume directly (no picker) when a known
// agent was already resolved before the confirm dialog appeared.
func TestHandleRestartConfirmUpdate_KnownAgentSkipsPicker(t *testing.T) {
	model := newTestModelWithSession(t, domain.Session{
		ID: "sess-1", RemoteHost: "devbox", TmuxSession: "PB-1", RepoName: "org/repo",
		Status: domain.SessionStatusActive, AgentName: "Codex CLI",
	})
	sess := model.allSessions[0]
	model.restartingSession = &sess
	model.sessionCfg.Agent = &domain.Agent{Name: "Codex CLI", Command: "codex"}
	model.state = viewStateRestartConfirm

	newModel, cmd := model.handleRestartConfirmUpdate(pressKey("y"))
	m := newModel.(*Model)
	if m.state != viewStateMain {
		t.Fatalf("state = %v, want viewStateMain (background restart, no loading screen)", m.state)
	}
	if _, ok := m.creatingSessions["sess-1"]; !ok {
		t.Fatal("expected a background-restart tracking entry for the session")
	}
	if cmd == nil {
		t.Fatal("expected a restart command")
	}
}

// Without a pre-resolved agent, confirming still goes to the picker exactly
// as before this change.
func TestHandleRestartConfirmUpdate_NoKnownAgentGoesToPicker(t *testing.T) {
	model := newTestModelWithSession(t, domain.Session{
		ID: "sess-1", RemoteHost: "devbox", TmuxSession: "PB-1", RepoName: "org/repo",
		Status: domain.SessionStatusActive,
	})
	sess := model.allSessions[0]
	model.restartingSession = &sess
	model.state = viewStateRestartConfirm

	newModel, cmd := model.handleRestartConfirmUpdate(pressKey("y"))
	m := newModel.(*Model)
	if m.loadingNext != viewStateRestartAgentPicker {
		t.Fatalf("loadingNext = %v, want viewStateRestartAgentPicker", m.loadingNext)
	}
	if cmd == nil {
		t.Fatal("expected a fetch-agents command")
	}
}

// A PTY-hosted session must be identifiable in the list: it is reattached and
// torn down differently from a tmux one, so the backend cannot be invisible.
func TestItemDescriptionMarksPTYBackend(t *testing.T) {
	pty := item{session: domain.Session{
		ID: "p1", RepoName: "app", RemoteHost: "devbox", Backend: domain.BackendPTY,
	}}
	if got := pty.Description(); !strings.Contains(got, "pty") {
		t.Errorf("pty session description should mark the backend, got %q", got)
	}

	// tmux is the default and stays unmarked, so the hint means something.
	tmux := item{session: domain.Session{
		ID: "t1", RepoName: "app", RemoteHost: "devbox", TmuxSession: "PB-1",
	}}
	if got := tmux.Description(); strings.Contains(got, "pty") {
		t.Errorf("tmux session must not be marked as pty, got %q", got)
	}
}

// The run-target picker's "b" toggle is the only way to run a pty session on a
// remote that defaults to tmux, and its choice has to survive the rest of the
// wizard. Two separate places used to discard it — resetSessionCfg rebuilt the
// config keeping only the host, and createSession then overwrote whatever was
// left with the remote's default — so the toggle was drawn but did nothing and
// every session came out tmux.
func TestSessionBackendChoiceSurvivesTheModePicker(t *testing.T) {
	cfg := &config.Config{Remotes: []config.Remote{{Host: "devbox", User: "code"}}}
	m := NewModel(cfg, nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.selectedRemote = cfg.Remotes[0]
	m.sessionCfg.SessionBackend = domain.BackendPTY

	// Any mode-picker branch rebuilds the config; the backend must come along.
	m.resetSessionCfg(domain.SessionConfig{})

	if m.sessionCfg.SessionBackend != domain.BackendPTY {
		t.Fatalf("backend = %q, want pty to survive the mode picker", m.sessionCfg.SessionBackend)
	}
	if m.sessionCfg.RemoteHost != "devbox" {
		t.Fatalf("host = %q, want the run target kept too", m.sessionCfg.RemoteHost)
	}
}

// A remote's session_backend is a default, not an override: it must not clobber
// a per-session choice already made in the picker.
func TestRemoteBackendDefaultDoesNotOverridePerSessionChoice(t *testing.T) {
	tmuxRemote := config.Remote{Host: "devbox", User: "code"} // no session_backend
	cfg := &config.Config{Remotes: []config.Remote{tmuxRemote}}
	m := NewModel(cfg, nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.selectedRemote = tmuxRemote

	// Chosen in the picker, against a remote that defaults to tmux.
	m.sessionCfg.SessionBackend = domain.BackendPTY
	m.summary.SetBackend(m.sessionCfg.SessionBackend)

	if got := m.summary.viewString(); !strings.Contains(got, "pty") {
		t.Errorf("the summary must show the chosen backend so it is verifiable before creating")
	}

	// And the reverse: an unset per-session backend inherits the remote's.
	ptyRemote := config.Remote{Host: "ptybox", User: "code", SessionBackend: domain.BackendPTY}
	m2 := NewModel(&config.Config{Remotes: []config.Remote{ptyRemote}}, nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m2.selectedRemote = ptyRemote
	m2.resetSessionCfg(domain.SessionConfig{})
	if m2.sessionCfg.SessionBackend != "" {
		t.Fatalf("an unchosen backend should stay empty until creation applies the remote default, got %q",
			m2.sessionCfg.SessionBackend)
	}
}

// A remote's session_backend is a default, not an override.
func TestResolveSessionBackend(t *testing.T) {
	cases := []struct{ chosen, remoteDefault, want string }{
		// The picker's choice wins, including choosing pty on a tmux remote —
		// the case that was silently discarded.
		{domain.BackendPTY, "", domain.BackendPTY},
		{domain.BackendPTY, domain.BackendTmux, domain.BackendPTY},
		// And explicitly choosing tmux on a pty remote must stick too.
		{domain.BackendTmux, domain.BackendPTY, domain.BackendTmux},
		// With no choice made, the remote's default applies.
		{"", domain.BackendPTY, domain.BackendPTY},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := resolveSessionBackend(c.chosen, c.remoteDefault); got != c.want {
			t.Errorf("resolveSessionBackend(%q, %q) = %q, want %q", c.chosen, c.remoteDefault, got, c.want)
		}
	}
}
