package usecase

import (
	"context"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// Acceptance tests for multi-repo sessions, piece P1. Children must not edit
// this file.

func workspaceRecords() []domain.WorktreeRecord {
	return []domain.WorktreeRecord{
		{RepoPath: "/repos/aiman", WorktreePath: "/repos/host-tab@ws/aiman", State: domain.WorktreeOK, Workspace: "ws-1"},
		{RepoPath: "/repos/aiman-phone", WorktreePath: "/repos/host-tab@ws/aiman-phone", State: domain.WorktreeOK, Workspace: "ws-1"},
		{RepoPath: "/repos/aiman", WorktreePath: "/repos/aiman@feature", State: domain.WorktreeOK, AimanID: "s-2"},
	}
}

// A member worktree of a workspace session is never a session of its own.
func TestDiscoverSkipsMembers(t *testing.T) {
	remote := &batchRemote{wtRecords: workspaceRecords()}
	sessions, err := NewSessionDiscoverer(remote, &recordingSyncEngine{}).Discover(context.Background(), "regent0")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "s-2" {
		t.Fatalf("Discover = %+v, want only s-2", sessions)
	}
}

func TestOrphanWorktreeSkipsMembers(t *testing.T) {
	remote := &batchRemote{wtRecords: workspaceRecords()}
	sessions, err := NewSessionDiscoverer(remote, &recordingSyncEngine{}).OrphanWorktreeSessions(context.Background(), "regent0")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions {
		if s.WorktreePath != "/repos/aiman@feature" {
			t.Errorf("orphan list offers member worktree %s", s.WorktreePath)
		}
	}
}
