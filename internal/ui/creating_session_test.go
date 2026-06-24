package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/usecase"
)

func newTestModelWithSummaryConfirmed(t *testing.T) *Model {
	t.Helper()
	cfg := &config.Config{Remotes: []config.Remote{{Host: "devbox", User: "code"}}}
	model := NewModel(cfg, []usecase.CheckResult{}, []domain.Session{}, &mockSessionRepo{}, nil, nil, nil)
	model.selectedRemote = cfg.Remotes[0]
	model.sessionCfg = domain.SessionConfig{
		IssueKey: "PB-1",
		Branch:   "feature/pb-1",
		Repo:     domain.Repo{Name: "org/repo"},
	}
	return model
}

func TestStartBackgroundCreate_InsertsPlaceholderAndReturnsToMain(t *testing.T) {
	model := newTestModelWithSummaryConfirmed(t)
	model.state = viewStateSummary

	cmd := model.startBackgroundCreate()
	if cmd == nil {
		t.Fatal("expected a background create command")
	}
	if model.state != viewStateMain {
		t.Fatalf("expected to return to main view, got %v", model.state)
	}
	if len(model.creatingSessions) != 1 {
		t.Fatalf("expected 1 tracked creation, got %d", len(model.creatingSessions))
	}
	if len(model.allSessions) != 1 {
		t.Fatalf("expected placeholder in session list, got %d sessions", len(model.allSessions))
	}
	placeholder := model.allSessions[0]
	if placeholder.Status != domain.SessionStatusProvisioning {
		t.Errorf("expected PROVISIONING placeholder, got %s", placeholder.Status)
	}
	if !model.isCreatingPlaceholder(placeholder.ID) {
		t.Error("expected placeholder to be tracked as creating")
	}
	// The placeholder must be selected so its steps show in the preview panel.
	sel, ok := model.list.SelectedItem().(item)
	if !ok || sel.session.ID != placeholder.ID {
		t.Error("expected the placeholder to be selected")
	}
}

func TestBackgroundCreate_StatusMessagesAccumulateSteps(t *testing.T) {
	model := newTestModelWithSummaryConfirmed(t)
	_ = model.startBackgroundCreate()
	id := model.allSessions[0].ID

	for _, s := range []string{"Creating session...", "Creating session...", "Cloning repo..."} {
		updated, _ := model.Update(sessionCreateMsg{placeholderID: id, status: s})
		model = updated.(*Model)
	}

	cs := model.creatingSessions[id]
	if cs == nil {
		t.Fatal("creation record missing")
	}
	// Immediate duplicates are collapsed.
	if len(cs.steps) != 2 {
		t.Fatalf("expected 2 deduplicated steps, got %d: %v", len(cs.steps), cs.steps)
	}
	if model.state != viewStateMain {
		t.Errorf("status updates must not change view state, got %v", model.state)
	}

	panel := model.renderCreatingPanel(cs, 80)
	if !strings.Contains(panel, "Cloning repo...") {
		t.Errorf("expected preview panel to contain the latest step, got:\n%s", panel)
	}
}

func TestBackgroundCreate_FailureMarksPlaceholder(t *testing.T) {
	model := newTestModelWithSummaryConfirmed(t)
	_ = model.startBackgroundCreate()
	id := model.allSessions[0].ID

	updated, _ := model.Update(sessionCreateMsg{placeholderID: id, err: errors.New("ssh: connect refused")})
	model = updated.(*Model)

	cs := model.creatingSessions[id]
	if cs == nil || !cs.failed {
		t.Fatal("expected creation to be marked failed")
	}
	if model.state != viewStateMain {
		t.Errorf("background failure must not hijack the view, got state %v", model.state)
	}
	if model.allSessions[0].Status != domain.SessionStatusError {
		t.Errorf("expected placeholder status ERROR, got %s", model.allSessions[0].Status)
	}
	panel := model.renderCreatingPanel(cs, 80)
	if !strings.Contains(panel, "ssh: connect refused") {
		t.Errorf("expected failure panel to contain the error, got:\n%s", panel)
	}
}

func TestBackgroundCreate_SuccessReplacesPlaceholder(t *testing.T) {
	model := newTestModelWithSummaryConfirmed(t)
	_ = model.startBackgroundCreate()
	id := model.allSessions[0].ID

	real := domain.Session{
		ID:          "real-1",
		TmuxSession: "pb-1",
		RepoName:    "org/repo",
		RemoteHost:  "devbox",
		Status:      domain.SessionStatusActive,
	}
	updated, _ := model.Update(sessionCreateMsg{placeholderID: id, session: real})
	model = updated.(*Model)

	if len(model.creatingSessions) != 0 {
		t.Error("expected creation record to be cleared on success")
	}
	if len(model.allSessions) != 1 || model.allSessions[0].ID != "real-1" {
		t.Fatalf("expected placeholder replaced by real session, got %+v", model.allSessions)
	}
	sel, ok := model.list.SelectedItem().(item)
	if !ok || sel.session.ID != "real-1" {
		t.Error("expected selection to follow the new session")
	}
}

func TestBackgroundCreate_WorktreeExistsAutoRecycles(t *testing.T) {
	model := newTestModelWithSummaryConfirmed(t)
	_ = model.startBackgroundCreate()
	id := model.allSessions[0].ID

	updated, _ := model.Update(sessionCreateMsg{placeholderID: id, err: errors.New("WORKTREE_EXISTS")})
	model = updated.(*Model)

	if model.state == viewStateWorktreeExists {
		t.Fatalf("expected worktree-exists to auto-resolve, but got dialog")
	}
	if !model.sessionCfg.AttachExisting {
		t.Errorf("expected sessionCfg.AttachExisting to be true for auto-recycling")
	}
}

func TestBackgroundCreate_PlaceholderSurvivesDiscovery(t *testing.T) {
	model := newTestModelWithSummaryConfirmed(t)
	_ = model.startBackgroundCreate()
	id := model.allSessions[0].ID
	model.state = viewStateLoading

	updated, _ := model.Update(discoveryResultMsg{sessions: nil, scannedHosts: map[string]bool{"devbox": true}})
	model = updated.(*Model)

	found := false
	for _, s := range model.allSessions {
		if s.ID == id {
			found = true
		}
	}
	if !found {
		t.Error("expected placeholder to survive a discovery refresh")
	}
}

func TestBackgroundCreate_ReadyToastAutoClears(t *testing.T) {
	model := newTestModelWithSummaryConfirmed(t)
	_ = model.startBackgroundCreate()
	id := model.allSessions[0].ID

	updated, _ := model.Update(sessionCreateMsg{placeholderID: id, session: domain.Session{ID: "real-1", TmuxSession: "pb-1", RemoteHost: "devbox"}})
	model = updated.(*Model)

	if !strings.Contains(model.snapshotToast, "is ready") {
		t.Fatalf("expected ready toast, got %q", model.snapshotToast)
	}

	// A stale clear timer (from an older toast) must not wipe the bar.
	updated, _ = model.Update(snapshotToastMsg{seq: model.snapshotToastSeq - 1})
	model = updated.(*Model)
	if model.snapshotToast == "" {
		t.Fatal("stale toast timer cleared a newer toast")
	}

	// The matching timer clears it.
	updated, _ = model.Update(snapshotToastMsg{seq: model.snapshotToastSeq})
	model = updated.(*Model)
	if model.snapshotToast != "" {
		t.Fatalf("expected toast cleared, still %q", model.snapshotToast)
	}
}
