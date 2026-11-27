package domain

import (
	"context"
	"path/filepath"
	"strings"
)

// WorkspaceMarkerFile is the file inside a member worktree's git dir that
// holds the id of the workspace session the worktree belongs to.
const WorkspaceMarkerFile = "aiman-workspace"

// SessionRepo is one repository of a multi-repo (workspace) session.
type SessionRepo struct {
	Repo         string
	Branch       string
	BaseBranch   string
	WorktreePath string
	Ord          int
}

// IsWorkspace reports whether the session spans several repositories.
func (s Session) IsWorkspace() bool { return len(s.Members) > 0 }

// MemberFor returns the member whose worktree is path or contains it. It
// compares cleaned paths on a separator boundary and never touches the disk.
func (s Session) MemberFor(path string) (SessionRepo, bool) {
	p := filepath.Clean(path)
	for _, m := range s.Members {
		root := filepath.Clean(m.WorktreePath)
		if m.WorktreePath == "" {
			continue
		}
		if p == root || strings.HasPrefix(p, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator)) {
			return m, true
		}
	}
	return SessionRepo{}, false
}

// RepoSpec is one repository a workspace session is created with: the
// owner/name slug and an optional base ref (empty is the repo's default
// branch).
type RepoSpec struct {
	Repo string
	Base string
}

// WorkspaceGit is what a workspace create needs from the git manager beyond
// RepositoryManager. CreateSession type-asserts it.
type WorkspaceGit interface {
	// BranchCheckedOut reports whether branch is checked out in any worktree
	// of repo. It makes the main clone healthy and fetched first.
	BranchCheckedOut(ctx context.Context, remote RemoteExecutor, repo Repo, branch string) (bool, error)
	// SetupWorkspaceMember adds a worktree of repo at exactly path on branch:
	// from origin/<branch>, tracking it, when origin has the branch, else a
	// new branch from base, else from the repo's default branch. Worktree.Base
	// reports the ref used.
	SetupWorkspaceMember(ctx context.Context, remote RemoteExecutor, repo Repo, branch, base, path string) (Worktree, error)
	// RemoveWorkspaceMember force-removes the worktree at path and prunes.
	RemoveWorkspaceMember(ctx context.Context, remote RemoteExecutor, repo Repo, path string) error
}
