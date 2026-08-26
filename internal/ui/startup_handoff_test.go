package ui

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/usecase"
)

func newTestStartupModel(repo domain.SessionRepository) StartupModel {
	cfg := &config.Config{Remotes: []config.Remote{{Host: "regent0"}}}
	return NewStartupModel(cfg, nil, repo, nil, nil, nil, "test")
}

// The dashboard opens on database contents alone. Neither the doctor checks nor
// the remote scan gate it: both are network round trips, and both report into
// the dashboard once they land.
func TestStartupHandsOffImmediately(t *testing.T) {
	repo := &startupSessionRepo{
		sessions: []domain.Session{
			{ID: "from-db", Name: "impl", Group: "WTB-1925", RemoteHost: "regent0", TmuxSession: "WTB-1"},
		},
	}

	next, _ := newTestStartupModel(repo).Update(startupReadyMsg{})

	dash, ok := next.(*Model)
	if !ok {
		t.Fatalf("expected an immediate handoff to the dashboard, got %T", next)
	}
	if len(dash.allSessions) != 1 || dash.allSessions[0].ID != "from-db" {
		t.Errorf("dashboard should open on database contents, got %+v", dash.allSessions)
	}
	if !dash.discoveryPending {
		t.Error("discoveryPending should be set when the dashboard opens before the scan lands")
	}
	foundHeader := false
	for _, it := range dash.list.Items() {
		si, ok := it.(item)
		if ok && si.header && si.session.Group == "WTB-1925" {
			foundHeader = true
		}
	}
	if !foundHeader {
		t.Fatal("startup dashboard must show persisted group headers before discovery")
	}
	if len(dash.doctorResults) != 0 {
		t.Errorf("no checks had landed yet, got %+v", dash.doctorResults)
	}
}

// Checks that land before the handoff must be carried across rather than lost.
func TestStartupCarriesEarlyCheckResults(t *testing.T) {
	var model any = newTestStartupModel(&startupSessionRepo{})

	sm := model.(StartupModel)
	next, _ := sm.Update(checkResultMsg(usecase.CheckResult{Name: "JIRA", Passed: true, Message: "ok"}))
	sm, ok := next.(StartupModel)
	if !ok {
		t.Fatalf("a check alone must not trigger handoff, got %T", next)
	}

	next, _ = sm.Update(startupReadyMsg{})
	dash, ok := next.(*Model)
	if !ok {
		t.Fatalf("expected handoff, got %T", next)
	}
	if len(dash.doctorResults) != 1 || dash.doctorResults[0].Name != "JIRA" {
		t.Errorf("expected the early JIRA result to be carried over, got %+v", dash.doctorResults)
	}
}

// Discovery landing first must still be applied, and must not leave the
// dashboard claiming a scan is running.
func TestStartupReplaysEarlyDiscovery(t *testing.T) {
	sm := newTestStartupModel(&startupSessionRepo{})

	next, _ := sm.Update(discoveryResultMsg{
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

	next, _ = sm.Update(startupReadyMsg{})
	dash, ok := next.(*Model)
	if !ok {
		t.Fatalf("expected handoff, got %T", next)
	}
	if dash.discoveryPending {
		t.Error("discoveryPending should be false when the scan already completed")
	}
}

func TestDashboardAppliesDiscoveryOnMainView(t *testing.T) {
	next, _ := newTestStartupModel(&startupSessionRepo{}).Update(startupReadyMsg{})
	dash := next.(*Model)
	if dash.state != viewStateMain {
		t.Fatalf("state %v", dash.state)
	}

	updated, _ := dash.Update(discoveryResultMsg{
		sessions:     []domain.Session{{ID: "live", RemoteHost: "regent0", TmuxSession: "WTB-2", Status: domain.SessionStatusActive}},
		scannedHosts: map[string]bool{"regent0": true},
	})
	dash = updated.(*Model)
	if len(dash.allSessions) != 1 || dash.allSessions[0].ID != "live" {
		t.Fatalf("main view must apply discovery, got %+v", dash.allSessions)
	}
}

// Results arriving after the handoff land on the dashboard.
func TestDashboardAcceptsLateCheckResults(t *testing.T) {
	next, _ := newTestStartupModel(&startupSessionRepo{}).Update(startupReadyMsg{})
	dash := next.(*Model)

	dash.applyCheckResult(usecase.CheckResult{Name: "SSH", Passed: true, Message: "1/1 reachable"})
	dash.applyCheckResult(usecase.CheckResult{Name: "Git/GitHub", Passed: false, Message: "not authenticated"})

	if len(dash.doctorResults) != 2 {
		t.Fatalf("expected 2 results, got %+v", dash.doctorResults)
	}

	// Re-running a check replaces its row rather than appending a duplicate.
	dash.applyCheckResult(usecase.CheckResult{Name: "Git/GitHub", Passed: true, Message: "authenticated"})
	if len(dash.doctorResults) != 2 {
		t.Fatalf("re-running a check should replace it, got %+v", dash.doctorResults)
	}
	for _, res := range dash.doctorResults {
		if res.Name == "Git/GitHub" && !res.Passed {
			t.Error("Git/GitHub result was not replaced with the newer one")
		}
	}
}
