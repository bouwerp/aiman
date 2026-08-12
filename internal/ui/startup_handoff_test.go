package ui

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/usecase"
)

func newTestStartupModel(repo domain.SessionRepository) StartupModel {
	cfg := &config.Config{Remotes: []config.Remote{{Host: "regent0"}}}
	m := NewStartupModel(cfg, nil, repo, nil, nil, nil, "test")
	return m
}

// The dashboard must open on the doctor checks alone. Discovery costs a remote
// round trip, and gating on it is what held the splash screen open.
func TestStartupHandsOffWithoutWaitingForDiscovery(t *testing.T) {
	repo := &startupSessionRepo{
		sessions: []domain.Session{
			{ID: "from-db", RemoteHost: "regent0", TmuxSession: "WTB-1"},
		},
	}
	m := newTestStartupModel(repo)

	var model any = m
	for _, name := range []string{"JIRA", "Git", "SSH"} {
		sm, ok := model.(StartupModel)
		if !ok {
			t.Fatalf("handed off after fewer than three checks (at %q)", name)
		}
		next, _ := sm.Update(checkResultMsg(usecase.CheckResult{Name: name, Passed: true}))
		model = next
	}

	dash, ok := model.(*Model)
	if !ok {
		t.Fatalf("expected handoff to the dashboard once the three checks landed, got %T", model)
	}
	if len(dash.allSessions) != 1 || dash.allSessions[0].ID != "from-db" {
		t.Errorf("dashboard should open on database contents, got %+v", dash.allSessions)
	}
	if !dash.discoveryPending {
		t.Error("discoveryPending should be set when the dashboard opens before the scan lands")
	}
}

// When discovery beats the checks, its result must still be applied rather than
// dropped, and the dashboard must not claim a scan is still running.
func TestStartupReplaysEarlyDiscovery(t *testing.T) {
	repo := &startupSessionRepo{}
	m := newTestStartupModel(repo)

	next, _ := m.Update(discoveryResultMsg{
		sessions:     []domain.Session{{ID: "discovered", RemoteHost: "regent0", TmuxSession: "WTB-2"}},
		scannedHosts: map[string]bool{"regent0": true},
	})
	sm, ok := next.(StartupModel)
	if !ok {
		t.Fatalf("discovery alone must not trigger handoff, got %T", next)
	}
	if !sm.discoveryDone {
		t.Fatal("expected discoveryDone to be recorded")
	}

	var model any = sm
	for _, name := range []string{"JIRA", "Git", "SSH"} {
		s := model.(StartupModel)
		model, _ = s.Update(checkResultMsg(usecase.CheckResult{Name: name, Passed: true}))
		_ = name
	}

	dash, ok := model.(*Model)
	if !ok {
		t.Fatalf("expected handoff to the dashboard, got %T", model)
	}
	if dash.discoveryPending {
		t.Error("discoveryPending should be false when the scan already completed")
	}
}

// Discovery must not decrement the gate; otherwise the splash could hand off
// after two checks and one scan.
func TestDiscoveryDoesNotConsumeAPendingSlot(t *testing.T) {
	m := newTestStartupModel(&startupSessionRepo{})
	before := m.pending

	next, _ := m.Update(discoveryResultMsg{scannedHosts: map[string]bool{}})
	sm, ok := next.(StartupModel)
	if !ok {
		t.Fatalf("unexpected handoff, got %T", next)
	}
	if sm.pending != before {
		t.Errorf("pending = %d, want it unchanged at %d", sm.pending, before)
	}
}
