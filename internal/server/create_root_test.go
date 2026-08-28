package server

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestCreateGitRootFromCallerWorktree(t *testing.T) {
	caller := &domain.Session{WorktreePath: "/home/code/repos/aiman@aiman-fixes-2808"}
	got := createGitRoot(caller, nil, "/home/code")
	if got != "/home/code/repos" {
		t.Fatalf("got %q, want the worktree parent", got)
	}
}

func TestCreateGitRootInfersFromExistingWorktreesWhenCallerUnknown(t *testing.T) {
	sessions := []domain.Session{
		{WorktreePath: "/home/code/repos/realfi@WTB-1"},
		{WorktreePath: "/home/code/repos/docs@notes"},
	}
	got := createGitRoot(nil, sessions, "/home/code")
	if got != "/home/code/repos" {
		t.Fatalf("got %q, want the shared worktree parent", got)
	}
}

func TestCreateGitRootIgnoresUnscopedPaths(t *testing.T) {
	got := createGitRoot(&domain.Session{WorktreePath: "/home/code/repos"}, nil, "/home/code")
	if got != "" {
		t.Fatalf("a main-clone path must not become the git root, got %q", got)
	}
}

func TestCreateGitRootIgnoresCallerWorktreeInHome(t *testing.T) {
	caller := &domain.Session{WorktreePath: "/home/code/treasury-admin-dapp@fix-bug"}
	sessions := []domain.Session{
		{WorktreePath: "/home/code/repos/treasury-admin-dapp@WTB-1"},
	}
	got := createGitRoot(caller, sessions, "/home/code")
	if got != "/home/code/repos" {
		t.Fatalf("a worktree in $HOME must not become the registry, got %q", got)
	}
}

func TestCreateGitRootFallsBackToServeRootWhenOnlyHomeWorktreesExist(t *testing.T) {
	sessions := []domain.Session{
		{WorktreePath: "/home/code/realfi@review-one"},
		{WorktreePath: "/home/code/realfi@review-two"},
	}
	got := createGitRoot(nil, sessions, "/home/code")
	if got != "" {
		t.Fatalf("$HOME worktrees must not out-vote the configured root, got %q", got)
	}
}

func TestCreateGitRootTreatsTrailingSlashHomeAsHome(t *testing.T) {
	caller := &domain.Session{WorktreePath: "/home/code/realfi@review"}
	if got := createGitRoot(caller, nil, "/home/code/"); got != "" {
		t.Fatalf("got %q, want home rejected regardless of trailing slash", got)
	}
}
