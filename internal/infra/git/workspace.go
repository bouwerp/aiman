package git

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/shellx"
)

var _ domain.WorkspaceGit = (*Manager)(nil)

// workspaceMainClone resolves repo's main clone under the remote root and
// makes it healthy and fetched, as SetupRemoteWorktree does.
func workspaceMainClone(ctx context.Context, remote domain.RemoteExecutor, repo domain.Repo) (string, error) {
	root := remote.GetRoot()
	if root == "" {
		return "", fmt.Errorf("remote root not configured")
	}
	repoPath := computeMainRepoPath(root, repo.Name)
	if err := ensureHealthyRepo(ctx, remote, repoPath, repo.URL); err != nil {
		return "", err
	}
	return repoPath, nil
}

// BranchCheckedOut reports whether branch is checked out in any worktree of
// repo, the main clone included. The main clone is made healthy and fetched
// first.
func (m *Manager) BranchCheckedOut(ctx context.Context, remote domain.RemoteExecutor, repo domain.Repo, branch string) (_ bool, err error) {
	defer func() { err = WrapRemoteError(err) }()
	repoPath, err := workspaceMainClone(ctx, remote, repo)
	if err != nil {
		return false, err
	}
	out, err := remote.Execute(ctx, fmt.Sprintf("git -C %s worktree list --porcelain", shellx.Quote(repoPath)))
	if err != nil {
		return false, fmt.Errorf("failed to list worktrees of %s: %w", repoPath, err)
	}
	return porcelainHasBranch(out, branch), nil
}

// porcelainHasBranch reports whether `git worktree list --porcelain` output
// has a worktree on refs/heads/<branch>. Only an exact line matches, so a
// branch named <branch>-2 does not.
func porcelainHasBranch(out, branch string) bool {
	want := "branch refs/heads/" + branch
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

// SetupWorkspaceMember adds a worktree of repo at exactly path on branch. A
// branch origin already has starts at origin/<branch> and tracks it (-B
// resets a stale local branch of that name); any other branch is cut from
// base, which names origin/<base> when origin has it, else from the repo's
// default branch. Worktree.Base is the ref the worktree started from.
func (m *Manager) SetupWorkspaceMember(ctx context.Context, remote domain.RemoteExecutor, repo domain.Repo, branch, base, path string) (_ domain.Worktree, err error) {
	defer func() { err = WrapRemoteError(err) }()
	if strings.TrimSpace(branch) == "" {
		return domain.Worktree{}, fmt.Errorf("workspace member of %s needs a branch", repo.Name)
	}
	if !filepath.IsAbs(path) {
		return domain.Worktree{}, fmt.Errorf("workspace member path %q is not absolute", path)
	}
	repoPath, err := workspaceMainClone(ctx, remote, repo)
	if err != nil {
		return domain.Worktree{}, err
	}

	// Stale metadata from an earlier removal would make worktree add fail.
	_, _ = remote.Execute(ctx, fmt.Sprintf("git -C %s worktree prune --expire=now 2>/dev/null || true", shellx.Quote(repoPath)))

	startPoint, track := "", ""
	switch {
	case remoteBranchExists(ctx, remote, repoPath, branch):
		startPoint, track = "origin/"+branch, "--track "
	case base != "":
		startPoint = base
		if !strings.HasPrefix(base, "origin/") && remoteBranchExists(ctx, remote, repoPath, base) {
			startPoint = "origin/" + base
		}
	default:
		startPoint = detectBaseBranch(ctx, remote, repoPath)
		if startPoint == "" {
			return domain.Worktree{}, fmt.Errorf("failed to create worktree: cannot determine default branch for %s: the repository may have no commits", repoPath)
		}
	}

	reportProgress(ctx, fmt.Sprintf("Creating worktree %s...", path))
	cmd := fmt.Sprintf("git -C %s worktree add %s-B %s -- %s %s", shellx.Quote(repoPath), track, shellx.Quote(branch), shellx.Quote(path), shellx.Quote(startPoint))
	if _, err := remote.Execute(ctx, cmd); err != nil {
		return domain.Worktree{}, fmt.Errorf("failed to create worktree %s: %w", path, err)
	}
	if err := prepareWorktreeForSession(ctx, remote, repoPath, path); err != nil {
		return domain.Worktree{}, err
	}
	return domain.Worktree{Path: path, Branch: branch, Base: startPoint}, nil
}

// RemoveWorkspaceMember force-removes the worktree at path from repo's main
// clone, then prunes its metadata. A path that is already gone is not an
// error.
func (m *Manager) RemoveWorkspaceMember(ctx context.Context, remote domain.RemoteExecutor, repo domain.Repo, path string) (err error) {
	defer func() { err = WrapRemoteError(err) }()
	root := remote.GetRoot()
	if root == "" {
		return fmt.Errorf("remote root not configured")
	}
	repoPath := computeMainRepoPath(root, repo.Name)
	q := shellx.Quote(repoPath)

	reportProgress(ctx, fmt.Sprintf("Removing worktree %s...", path))
	if _, rmErr := remote.Execute(ctx, fmt.Sprintf("git -C %s worktree remove --force %s", q, shellx.Quote(path))); rmErr != nil {
		if remote.ValidateDir(ctx, path) == nil {
			return fmt.Errorf("failed to remove worktree %s: %w", path, rmErr)
		}
	}
	if _, err := remote.Execute(ctx, fmt.Sprintf("git -C %s worktree prune", q)); err != nil {
		return fmt.Errorf("failed to prune worktrees of %s: %w", repoPath, err)
	}
	return nil
}
