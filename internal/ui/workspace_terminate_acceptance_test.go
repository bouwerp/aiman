package ui

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// Acceptance tests for multi-repo sessions, piece P4 (TUI terminate). Children
// must not edit this file.
//
// The TUI's own teardown keys worktree removal on RepoName, which a workspace
// session leaves empty, so a workspace session is torn down by serve's
// session.terminate instead. terminateViaServe picks that path and
// terminateServeParams builds the call.

func wsTerminateSession() domain.Session {
	return domain.Session{
		ID: "ws-1", Branch: "host-tab", WorktreePath: "/w/host-tab@ws", WorkingDirectory: "/w/host-tab@ws",
		Members: []domain.SessionRepo{
			{Repo: "bouwerp/aiman", Branch: "host-tab", WorktreePath: "/w/host-tab@ws/aiman"},
			{Repo: "bouwerp/aiman-phone", Branch: "host-tab", WorktreePath: "/w/host-tab@ws/aiman-phone", Ord: 1},
		},
	}
}

func TestTerminateWorkspaceUsesServe(t *testing.T) {
	if !terminateViaServe(wsTerminateSession()) {
		t.Fatal("a workspace session must be terminated through serve's session.terminate")
	}
	plain := []domain.Session{
		{ID: "s1", RepoName: "o/r", WorktreePath: "/w/r@b", WorkingDirectory: "/w/r@b"},
		{ID: "s2", WorkingDirectory: "/w"}, // ad hoc
	}
	for _, s := range plain {
		if terminateViaServe(s) {
			t.Fatalf("session %s is not a workspace session; the TUI tears it down itself", s.ID)
		}
	}
}

// The operator's forced terminate becomes discard on the call.
func TestTerminateWorkspaceUsesServeDiscard(t *testing.T) {
	s := wsTerminateSession()
	for _, forced := range []bool{false, true} {
		p := terminateServeParams(s, forced)
		if p["session"] != s.ID {
			t.Fatalf("session = %v", p["session"])
		}
		if d, _ := p["discard"].(bool); d != forced {
			t.Fatalf("forced %v: discard = %v", forced, p["discard"])
		}
	}
}
