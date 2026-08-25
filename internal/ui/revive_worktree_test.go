package ui

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestUnseenWorktreeSessions_ExcludesKnownIDs(t *testing.T) {
	sessions := []domain.Session{
		{ID: "known-1", WorktreePath: "/repos/app@one"},
		{ID: "new-1", WorktreePath: "/repos/app@two"},
		{ID: "new-2", WorktreePath: "/repos/app@three"},
	}
	knownIDs := map[string]bool{"known-1": true}

	got := unseenWorktreeSessions(sessions, knownIDs)
	if len(got) != 2 {
		t.Fatalf("expected 2 unseen sessions, got %d: %+v", len(got), got)
	}
	for _, s := range got {
		if s.ID == "known-1" {
			t.Errorf("expected known-1 to be excluded, got %+v", got)
		}
	}
}

func TestUnseenWorktreeSessions_EmptyKnownIDsKeepsAll(t *testing.T) {
	sessions := []domain.Session{{ID: "a"}, {ID: "b"}}
	got := unseenWorktreeSessions(sessions, map[string]bool{})
	if len(got) != 2 {
		t.Fatalf("expected both sessions kept, got %+v", got)
	}
}

func newTestModelForRevive(t *testing.T) *Model {
	t.Helper()
	sess := domain.Session{
		ID: "wt-1", RemoteHost: "devbox", TmuxSession: "app-feature",
		RepoName: "app", WorktreePath: "/repos/app@feature", WorkingDirectory: "/repos/app@feature",
		Status: domain.SessionStatusInactive,
	}
	return newTestModelWithSession(t, sess)
}

// Zero candidates falls back to the full agent picker rather than guessing.
func TestChooseReviveTarget_NoCandidatesGoesToPicker(t *testing.T) {
	model := newTestModelForRevive(t)
	entry := reviveItem{session: model.allSessions[0]}

	newModel, cmd := model.chooseReviveTarget(entry)
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

// One candidate revives immediately, no picker and no confirm, and the
// revive runs in the background like session creation — the dashboard stays
// usable instead of blocking on a loading screen.
func TestChooseReviveTarget_OneCandidateRevivesDirectly(t *testing.T) {
	model := newTestModelForRevive(t)
	entry := reviveItem{session: model.allSessions[0], candidates: []string{"Codex CLI"}}

	newModel, cmd := model.chooseReviveTarget(entry)
	m := newModel.(*Model)
	if m.state != viewStateMain {
		t.Fatalf("state = %v, want viewStateMain (background revive, no loading screen)", m.state)
	}
	if _, ok := m.creatingSessions["wt-1"]; !ok {
		t.Fatal("expected a background-restart tracking entry for the worktree")
	}
	if m.sessionCfg.Agent == nil || m.sessionCfg.Agent.Name != "Codex CLI" {
		t.Fatalf("expected sessionCfg.Agent to resolve to Codex CLI, got %+v", m.sessionCfg.Agent)
	}
	if m.restartingSession == nil || m.restartingSession.ID != "wt-1" {
		t.Fatalf("expected restartingSession to be the chosen worktree, got %+v", m.restartingSession)
	}
	if cmd == nil {
		t.Fatal("expected a restart command")
	}
}

// Multiple candidates show the short pick-list rather than guessing.
func TestChooseReviveTarget_MultipleCandidatesShowsPickList(t *testing.T) {
	model := newTestModelForRevive(t)
	entry := reviveItem{session: model.allSessions[0], candidates: []string{"Claude Code", "Codex CLI"}}

	newModel, _ := model.chooseReviveTarget(entry)
	m := newModel.(*Model)
	if m.state != viewStateReviveAgentPick {
		t.Fatalf("state = %v, want viewStateReviveAgentPick", m.state)
	}
	if len(m.revivePickCandidates) != 2 {
		t.Fatalf("expected 2 pending candidates, got %+v", m.revivePickCandidates)
	}
	if m.sessionCfg.Agent != nil {
		t.Fatalf("expected no agent resolved yet, got %+v", m.sessionCfg.Agent)
	}
}

// Picking a numbered candidate from the short list resumes with it directly.
func TestHandleReviveAgentPickUpdate_NumberKeyRevives(t *testing.T) {
	model := newTestModelForRevive(t)
	model.prepareRestartTarget(model.allSessions[0])
	model.revivePickCandidates = []string{"Claude Code", "Codex CLI"}
	model.state = viewStateReviveAgentPick

	newModel, cmd := model.handleReviveAgentPickUpdate(pressKey("2"))
	m := newModel.(*Model)
	if m.sessionCfg.Agent == nil || m.sessionCfg.Agent.Name != "Codex CLI" {
		t.Fatalf("expected Codex CLI resolved, got %+v", m.sessionCfg.Agent)
	}
	if cmd == nil {
		t.Fatal("expected a restart command")
	}
}

// "o" always opens the full picker even when candidates were offered.
func TestHandleReviveAgentPickUpdate_OtherOpensFullPicker(t *testing.T) {
	model := newTestModelForRevive(t)
	model.prepareRestartTarget(model.allSessions[0])
	model.revivePickCandidates = []string{"Claude Code", "Codex CLI"}
	model.state = viewStateReviveAgentPick

	newModel, cmd := model.handleReviveAgentPickUpdate(pressKey("o"))
	m := newModel.(*Model)
	if m.loadingNext != viewStateRestartAgentPicker {
		t.Fatalf("loadingNext = %v, want viewStateRestartAgentPicker", m.loadingNext)
	}
	if cmd == nil {
		t.Fatal("expected a fetch-agents command")
	}
}

// esc backs out to the list without picking anything.
func TestHandleReviveAgentPickUpdate_EscGoesBackToList(t *testing.T) {
	model := newTestModelForRevive(t)
	model.state = viewStateReviveAgentPick
	model.revivePickCandidates = []string{"Claude Code", "Codex CLI"}

	newModel, _ := model.handleReviveAgentPickUpdate(pressKey("esc"))
	m := newModel.(*Model)
	if m.state != viewStateReviveList {
		t.Fatalf("state = %v, want viewStateReviveList", m.state)
	}
}
