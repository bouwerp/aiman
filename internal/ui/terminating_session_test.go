package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	tea "github.com/charmbracelet/bubbletea"
)

// newTestModelWithTerminatableSession builds a model with one session whose
// remote is absent from config, so the terminate precheck passes without SSH.
func newTestModelWithTerminatableSession(t *testing.T) (*Model, domain.Session) {
	t.Helper()
	s := domain.Session{
		ID:           "sess-1",
		TmuxSession:  "pb-1",
		RemoteHost:   "gone-host",
		WorktreePath: "/home/dev/pb-1",
		RepoName:     "org/repo",
	}
	cfg := &config.Config{Remotes: []config.Remote{{Host: "other-host"}}}
	model := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	model.list.Select(0)
	return model, s
}

func TestBackgroundTerminate_ConfirmReturnsToMain(t *testing.T) {
	model, s := newTestModelWithTerminatableSession(t)
	model.state = viewStateTerminateConfirm

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	model = updated.(*Model)

	if model.state != viewStateMain {
		t.Fatalf("expected to return to main view immediately, got %v", model.state)
	}
	if cmd == nil {
		t.Fatal("expected the first terminate step to be dispatched")
	}
	ts := model.terminatingSessions[s.ID]
	if ts == nil {
		t.Fatal("expected termination to be tracked")
	}
	if ts.forced {
		t.Error("'y' must not force-terminate")
	}
	if len(ts.steps) != 7 {
		t.Errorf("expected 7 non-forced steps, got %d", len(ts.steps))
	}
	// Session must still be listed, marked as terminating.
	if len(model.allSessions) != 1 {
		t.Fatalf("session must stay in the list while terminating, got %d", len(model.allSessions))
	}
	sel, ok := model.list.SelectedItem().(item)
	if !ok || sel.activity != "terminating" {
		t.Errorf("expected list item marked terminating, got %+v", sel.activity)
	}
}

func TestBackgroundTerminate_ForcedUsesForcedSteps(t *testing.T) {
	model, s := newTestModelWithTerminatableSession(t)
	model.state = viewStateTerminateConfirm

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	model = updated.(*Model)

	ts := model.terminatingSessions[s.ID]
	if ts == nil || !ts.forced {
		t.Fatal("expected forced termination to be tracked")
	}
	if len(ts.steps) != 8 {
		t.Errorf("expected 8 forced steps, got %d", len(ts.steps))
	}
}

func TestBackgroundTerminate_StepsProgressAndComplete(t *testing.T) {
	model, s := newTestModelWithTerminatableSession(t)
	model.state = viewStateTerminateConfirm
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	model = updated.(*Model)
	ts := model.terminatingSessions[s.ID]

	// Steps progress without changing the view, even from another state.
	model.state = viewStateMenu
	updated, cmd := model.Update(terminateStepMsg{sessionID: s.ID, index: 0})
	model = updated.(*Model)
	if ts.idx != 1 {
		t.Fatalf("expected step index 1, got %d", ts.idx)
	}
	if cmd == nil {
		t.Fatal("expected next step to be dispatched")
	}
	if model.state != viewStateMenu {
		t.Errorf("background step must not change view state, got %v", model.state)
	}

	// Preview panel shows progress.
	panel := model.renderTerminatingPanel(ts, 80)
	if !strings.Contains(panel, "Killing tmux session") {
		t.Errorf("expected step labels in panel, got:\n%s", panel)
	}

	// Drive remaining steps to completion (one had an error).
	for i := 1; i < len(ts.steps); i++ {
		var err error
		if i == 2 {
			err = errTest
		}
		updated, _ = model.Update(terminateStepMsg{sessionID: s.ID, index: i, err: err})
		model = updated.(*Model)
	}

	if len(model.terminatingSessions) != 0 {
		t.Error("expected termination tracking cleared on completion")
	}
	if len(model.allSessions) != 0 {
		t.Errorf("expected session removed from list, got %d", len(model.allSessions))
	}
	if !strings.Contains(model.snapshotToast, "1 error") {
		t.Errorf("expected error-count toast, got %q", model.snapshotToast)
	}
}

func TestBackgroundTerminate_ActionKeysBlocked(t *testing.T) {
	model, s := newTestModelWithTerminatableSession(t)
	model.state = viewStateTerminateConfirm
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	model = updated.(*Model)

	// ctrl+k on a terminating session must not re-open the terminate dialog.
	updated, _, handled := model.handleMainKeyMsg(tea.KeyMsg{Type: tea.KeyCtrlK})
	model = updated.(*Model)
	if !handled {
		t.Fatal("expected ctrl+k to be intercepted for terminating session")
	}
	if model.state != viewStateMain {
		t.Errorf("expected to stay in main view, got %v", model.state)
	}
	if _, still := model.terminatingSessions[s.ID]; !still {
		t.Error("termination tracking must be unaffected by blocked keys")
	}
}

var errTest = errTestType{}

type errTestType struct{}

func (errTestType) Error() string { return "boom" }

func TestRunTerminateStep_MainRepoWorktreeIsSkippedNotFailed(t *testing.T) {
	cfg := &config.Config{Remotes: []config.Remote{{Host: "devbox", Root: "/home/code/repos"}}}
	model := NewModel(cfg, nil, nil, &mockSessionRepo{}, nil, nil, nil)
	s := domain.Session{
		ID:           "sess-2",
		TmuxSession:  "confirmrdsdetails",
		RemoteHost:   "devbox",
		WorktreePath: "/home/code/repos/confirmrdsdetails",
		RepoName:     "confirmrdsdetails",
	}

	err := model.runTerminateStep(3, s, false)
	var skip skipReason
	if err == nil || !errors.As(err, &skip) {
		t.Fatalf("expected a skipReason for main-repository worktree, got %v", err)
	}
	if !strings.Contains(string(skip), "main repository") {
		t.Errorf("skip reason should mention main repository, got %q", skip)
	}
}

func TestBackgroundTerminate_SafetySkipIsNotAnError(t *testing.T) {
	model, s := newTestModelWithTerminatableSession(t)
	model.state = viewStateTerminateConfirm
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	model = updated.(*Model)
	ts := model.terminatingSessions[s.ID]

	for i := 0; i < len(ts.steps); i++ {
		var err error
		if i == 3 { // worktree removal declined by a safety check
			err = skipReason("repository /home/dev/pb-1 left in place (main repository)")
		}
		updated, _ = model.Update(terminateStepMsg{sessionID: s.ID, index: i, err: err})
		model = updated.(*Model)
	}

	if len(model.allSessions) != 0 {
		t.Errorf("expected session removed from list, got %d", len(model.allSessions))
	}
	if model.snapshotToastError {
		t.Errorf("safety skip must not produce an error toast, got %q", model.snapshotToast)
	}
	if !strings.Contains(model.snapshotToast, "left in place") {
		t.Errorf("toast should mention the skipped cleanup, got %q", model.snapshotToast)
	}
}
