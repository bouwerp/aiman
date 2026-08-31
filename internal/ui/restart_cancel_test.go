package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// A PTY restart that finishes without placeholderID used to leave the
// creatingSessions entry forever — the UI stayed on "Restarting agent…" even
// though the remote pane had already been replaced. Success must clear tracking
// when the returned session ID matches the in-flight restart.
func TestBackgroundRestart_SuccessClearsTrackingEvenWithoutPlaceholderID(t *testing.T) {
	model := newTestModelWithSession(t, domain.Session{
		ID: "sess-1", RemoteHost: "devbox", TmuxSession: "evm-indexer-investigation",
		RepoName: "org/repo", Status: domain.SessionStatusActive, AgentName: "Claude Code",
		Backend: domain.BackendPTY, WorkingDirectory: "/home/code/repos/repo",
	})
	sess := model.allSessions[0]
	model.restartingSession = &sess
	model.sessionCfg.Agent = &domain.Agent{Name: "Codex CLI", Command: "codex"}
	_ = model.startBackgroundRestart()

	if _, ok := model.creatingSessions["sess-1"]; !ok {
		t.Fatal("expected restart tracking entry")
	}

	// Reproduce the PTY-path bug: success message omitted placeholderID.
	live := sess
	live.AgentName = "Codex CLI"
	live.Status = domain.SessionStatusActive
	updated, _ := model.Update(sessionCreateMsg{session: live})
	model = updated.(*Model)

	if _, ok := model.creatingSessions["sess-1"]; ok {
		t.Fatal("restart tracking must clear when the finished session matches, even without placeholderID")
	}
	if model.allSessions[0].AgentName != "Codex CLI" {
		t.Fatalf("expected agent updated to Codex CLI, got %q", model.allSessions[0].AgentName)
	}
}

// ctrl+k on an in-flight restart must abort the goroutine and restore the
// session row; unlike a failed create placeholder, the real session stays.
func TestCancelBackgroundRestart_CtrlKAbortsAndRestoresSession(t *testing.T) {
	model := newTestModelWithSession(t, domain.Session{
		ID: "sess-1", RemoteHost: "devbox", TmuxSession: "evm-indexer-investigation",
		RepoName: "org/repo", Status: domain.SessionStatusActive, AgentName: "Claude Code",
		Backend: domain.BackendPTY, WorkingDirectory: "/home/code/repos/repo",
	})
	sess := model.allSessions[0]
	model.restartingSession = &sess
	model.sessionCfg.Agent = &domain.Agent{Name: "Codex CLI", Command: "codex"}
	_ = model.startBackgroundRestart()

	cs := model.creatingSessions["sess-1"]
	if cs == nil {
		t.Fatal("expected restart tracking entry")
	}
	canceled := false
	cs.cancel = func() { canceled = true }

	// Select the session so ctrl+k targets it.
	items := model.list.Items()
	for i, it := range items {
		if si, ok := it.(item); ok && si.session.ID == "sess-1" {
			model.list.Select(i)
			break
		}
	}

	_, _, handled := model.handleMainKeyMsg(pressKey("ctrl+k"))
	if !handled {
		t.Fatal("expected ctrl+k to be handled for an in-flight restart")
	}
	if !canceled {
		t.Fatal("expected the restart context to be canceled")
	}
	if _, ok := model.creatingSessions["sess-1"]; ok {
		t.Fatal("expected restart tracking to be cleared")
	}
	if len(model.allSessions) != 1 || model.allSessions[0].ID != "sess-1" {
		t.Fatalf("restart cancel must keep the real session row, got %+v", model.allSessions)
	}
	if model.allSessions[0].Status != domain.SessionStatusActive {
		t.Fatalf("expected status restored to ACTIVE, got %s", model.allSessions[0].Status)
	}
}

// ctrl+k on an in-flight create (ephemeral placeholder) removes the row.
func TestCancelBackgroundCreate_CtrlKRemovesPlaceholder(t *testing.T) {
	model := newTestModelWithSummaryConfirmed(t)
	_ = model.startBackgroundCreate()
	id := model.allSessions[0].ID

	cs := model.creatingSessions[id]
	if cs == nil {
		t.Fatal("expected create tracking entry")
	}
	canceled := false
	cs.cancel = func() { canceled = true }

	items := model.list.Items()
	for i, it := range items {
		if si, ok := it.(item); ok && si.session.ID == id {
			model.list.Select(i)
			break
		}
	}

	_, _, handled := model.handleMainKeyMsg(pressKey("ctrl+k"))
	if !handled {
		t.Fatal("expected ctrl+k to cancel an in-flight create")
	}
	if !canceled {
		t.Fatal("expected the create context to be canceled")
	}
	if len(model.creatingSessions) != 0 || len(model.allSessions) != 0 {
		t.Fatalf("create cancel must drop the placeholder, got sessions=%d creating=%d",
			len(model.allSessions), len(model.creatingSessions))
	}
}

// The restart progress panel must tell the user how to cancel.
func TestRenderCreatingPanel_RestartShowsCancelHint(t *testing.T) {
	model := newTestModelWithSession(t, domain.Session{
		ID: "sess-1", RemoteHost: "devbox", TmuxSession: "feat",
		Status: domain.SessionStatusActive,
	})
	sess := model.allSessions[0]
	model.restartingSession = &sess
	model.sessionCfg.Agent = &domain.Agent{Name: "Codex CLI", Command: "codex"}
	_ = model.startBackgroundRestart()

	cs := model.creatingSessions["sess-1"]
	cs.addStep("Restarting agent in feat...")
	panel := model.renderCreatingPanel(cs, 80)
	if !strings.Contains(strings.ToLower(panel), "ctrl+k") {
		t.Fatalf("in-progress panel must mention ctrl+k to cancel, got:\n%s", panel)
	}
	if !strings.Contains(strings.ToLower(panel), "restart") {
		t.Fatalf("restart panel should say restarting, got:\n%s", panel)
	}
}

// Late success after cancel must not resurrect a canceled restart entry.
func TestBackgroundRestart_LateSuccessAfterCancelIsIgnored(t *testing.T) {
	model := newTestModelWithSession(t, domain.Session{
		ID: "sess-1", RemoteHost: "devbox", TmuxSession: "feat",
		Status: domain.SessionStatusActive, AgentName: "Claude Code",
		Backend: domain.BackendPTY,
	})
	sess := model.allSessions[0]
	model.restartingSession = &sess
	model.sessionCfg.Agent = &domain.Agent{Name: "Codex CLI", Command: "codex"}
	_ = model.startBackgroundRestart()

	ctx, cancel := context.WithCancel(context.Background())
	model.creatingSessions["sess-1"].cancel = cancel
	model.cancelBackgroundOperation("sess-1")
	<-ctx.Done() // cancel was invoked

	live := sess
	live.AgentName = "Codex CLI"
	updated, _ := model.Update(sessionCreateMsg{placeholderID: "sess-1", session: live})
	model = updated.(*Model)

	if _, ok := model.creatingSessions["sess-1"]; ok {
		t.Fatal("canceled restart must stay cleared after a late success message")
	}
}
