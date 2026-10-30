package ui

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func reviewRow() domain.Session {
	return domain.Session{
		ID: "rv-1", RemoteHost: "devbox", TerminalName: "pr-7", ReviewPR: "o/r#7",
		WorktreePath: "/w/r@pr-7", WorkingDirectory: "/h/.aiman/pr-reviews/rv-1/work",
		Status: domain.SessionStatusActive, AgentName: "claude",
	}
}

// c on a review session is refused: it runs in its own directory, and a
// directory under the PR checkout would launch the agent there.
func TestChangeDirRefusedForReview(t *testing.T) {
	model := newTestModelWithSession(t, reviewRow())
	newModel, _, handled := model.handleSessionManageKey(pressKey("c"))
	if !handled {
		t.Fatal("c not handled")
	}
	m := newModel.(*Model)
	if m.state != viewStateMain || m.changingDirSession != nil {
		t.Fatalf("state %v changing %v, want refused", m.state, m.changingDirSession)
	}
	if !strings.Contains(m.snapshotToast, "review sessions run in their own directory") {
		t.Fatalf("toast %q", m.snapshotToast)
	}
}

// The confirm step refuses too, whatever led to it.
func TestChangeDirConfirmRefusedForReview(t *testing.T) {
	model := newTestModelWithSession(t, reviewRow())
	s := reviewRow()
	model.changingDirSession = &s
	model.sessionCfg.Directory = "/w/r@pr-7/sub"
	model.state = viewStateChangeDirConfirm
	newModel, _ := model.handleChangeDirConfirmUpdate(pressKey("y"))
	m := newModel.(*Model)
	if m.restartingSession != nil || m.state != viewStateMain {
		t.Fatalf("restart started: state %v", m.state)
	}
}
