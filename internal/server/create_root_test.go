package server

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestCreateGitRootFromCallerWorktree(t *testing.T) {
	caller := &domain.Session{WorktreePath: "/home/code/repos/aiman@aiman-fixes-2808"}
	got := createGitRoot(caller, nil)
	if got != "/home/code/repos" {
		t.Fatalf("got %q, want the worktree parent", got)
	}
}

func TestCreateGitRootInfersFromExistingWorktreesWhenCallerUnknown(t *testing.T) {
	sessions := []domain.Session{
		{WorktreePath: "/home/code/repos/realfi@WTB-1"},
		{WorktreePath: "/home/code/repos/docs@notes"},
	}
	got := createGitRoot(nil, sessions)
	if got != "/home/code/repos" {
		t.Fatalf("got %q, want the shared worktree parent", got)
	}
}

func TestCreateGitRootIgnoresUnscopedPaths(t *testing.T) {
	got := createGitRoot(&domain.Session{WorktreePath: "/home/code/repos"}, nil)
	if got != "" {
		t.Fatalf("a main-clone path must not become the git root, got %q", got)
	}
}
