package ui

import (
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
// — no agent picker, no confirmation dialog.
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
	if m.state != viewStateLoading {
		t.Fatalf("state = %v, want viewStateLoading", m.state)
	}
	if m.loadingNext != viewStateMain {
		t.Fatalf("loadingNext = %v, want viewStateMain (no picker)", m.loadingNext)
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
	if m.state != viewStateLoading {
		t.Fatalf("state = %v, want viewStateLoading", m.state)
	}
	if m.loadingNext != viewStateMain {
		t.Fatalf("loadingNext = %v, want viewStateMain (no picker)", m.loadingNext)
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
