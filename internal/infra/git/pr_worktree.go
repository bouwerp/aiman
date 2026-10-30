package git

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
)

// SetupRemotePRWorktree puts pull request number of repo in the worktree
// <repo>@pr-<n>, detached at the PR head, then soft-resets it to the merge
// base with origin/<base> so the PR's changes are staged and every editor's
// gutter marks them. The head is fetched as pull/<n>/head from origin, which
// also reaches a PR from a fork. An existing worktree of that name is reused:
// it fetches the head itself and is checked out there by force.
func (m *Manager) SetupRemotePRWorktree(ctx context.Context, remote domain.RemoteExecutor, repo domain.Repo, number int, base string) (domain.Worktree, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return domain.Worktree{}, errors.New("a PR worktree needs the PR's base branch")
	}
	if number <= 0 {
		return domain.Worktree{}, fmt.Errorf("PR number %d is not positive", number)
	}
	root := remote.GetRoot()
	if root == "" {
		return domain.Worktree{}, errors.New("remote root not configured")
	}
	repoPath := computeMainRepoPath(root, repo.Name)
	if err := ensureHealthyRepo(ctx, remote, repoPath, repo.URL); err != nil {
		return domain.Worktree{}, err
	}
	dir, err := scopedWorktreeDir(repo.Name, fmt.Sprintf("pr-%d", number))
	if err != nil {
		return domain.Worktree{}, err
	}
	worktreePath := repoPath + "/../" + dir
	if err := checkoutPRHead(ctx, remote, repoPath, worktreePath, number); err != nil {
		return domain.Worktree{}, err
	}
	resolved := worktreePath
	if out, err := remote.Execute(ctx, "realpath "+shellQuote(worktreePath)); err == nil && strings.TrimSpace(out) != "" {
		resolved = strings.TrimSpace(out)
	}
	if err := StagePR(ctx, remote, resolved, base); err != nil {
		return domain.Worktree{}, err
	}
	if err := excludeAimanFiles(ctx, remote, resolved); err != nil {
		return domain.Worktree{}, err
	}
	if err := prepareWorktreeForSession(ctx, remote, repoPath, resolved); err != nil {
		return domain.Worktree{}, err
	}
	return domain.Worktree{Path: resolved, Base: base}, nil
}

// checkoutPRHead leaves worktreePath detached at pull/<number>/head. A healthy
// worktree of repoPath there is reused; anything else at that path is an
// error rather than something to delete.
func checkoutPRHead(ctx context.Context, remote domain.RemoteExecutor, repoPath, worktreePath string, number int) error {
	pull := fmt.Sprintf("pull/%d/head", number)
	owned, healthy, err := worktreePathBelongsToRepo(ctx, remote, repoPath, worktreePath)
	if err != nil {
		return err
	}
	if owned && healthy {
		// FETCH_HEAD is per worktree, so the reused worktree fetches for itself.
		wt := shellQuote(worktreePath)
		if out, err := remote.Execute(ctx, "git -C "+wt+" fetch -q origin "+pull+" 2>&1"); err != nil {
			return fmt.Errorf("fetch %s: %w: %s", pull, err, strings.TrimSpace(out))
		}
		if out, err := remote.Execute(ctx, "git -C "+wt+" checkout -q --force --detach FETCH_HEAD 2>&1"); err != nil {
			return fmt.Errorf("check out %s: %w: %s", pull, err, strings.TrimSpace(out))
		}
		return nil
	}
	if healthy {
		return fmt.Errorf("%s is a worktree of another repository", worktreePath)
	}
	exists, _ := remote.Execute(ctx, fmt.Sprintf("bash -c 'if [ -d %q ]; then echo EXISTS; fi'", worktreePath))
	if strings.Contains(exists, "EXISTS") {
		if err := safeRmWorktree(ctx, remote, worktreePath, repoPath); err != nil {
			return fmt.Errorf("remove broken PR worktree: %w", err)
		}
	}
	repoQ := shellQuote(repoPath)
	_, _ = remote.Execute(ctx, "git -C "+repoQ+" worktree prune --expire=now 2>/dev/null || true")
	if out, err := remote.Execute(ctx, "git -C "+repoQ+" fetch -q origin "+pull+" 2>&1"); err != nil {
		return fmt.Errorf("fetch %s: %w: %s", pull, err, strings.TrimSpace(out))
	}
	if out, err := remote.Execute(ctx, "git -C "+repoQ+" worktree add -q --detach "+shellQuote(worktreePath)+" FETCH_HEAD 2>&1"); err != nil {
		return fmt.Errorf("add PR worktree: %w: %s", err, strings.TrimSpace(out))
	}
	return nil
}

// excludeAimanFiles adds the .aiman_* glob to the repository's info/exclude,
// so serve's files stay out of git status while .gitignore, a file the PR
// may change, stays untouched.
func excludeAimanFiles(ctx context.Context, remote domain.RemoteExecutor, path string) error {
	line := shellQuote(domain.AimanGeneratedFileGlob)
	cmd := "ex=$(git -C " + shellQuote(path) + " rev-parse --path-format=absolute --git-path info/exclude) && " +
		`mkdir -p "$(dirname "$ex")" && { grep -qxF ` + line + ` "$ex" 2>/dev/null || echo ` + line + ` >> "$ex"; }`
	if out, err := remote.Execute(ctx, cmd); err != nil {
		return fmt.Errorf("exclude aiman files: %w: %s", err, strings.TrimSpace(out))
	}
	return nil
}

// StagePR soft-resets the worktree at path to its merge base with
// origin/<base>, so HEAD is the base and the index and files are the PR head.
func StagePR(ctx context.Context, remote domain.RemoteExecutor, path, base string) error {
	wt := shellQuote(path)
	cmd := "mb=$(git -C " + wt + " merge-base HEAD " + shellQuote("origin/"+base) + ") && git -C " + wt + ` reset -q --soft "$mb" 2>&1`
	if out, err := remote.Execute(ctx, cmd); err != nil {
		return fmt.Errorf("stage the PR against origin/%s: %w: %s", base, err, strings.TrimSpace(out))
	}
	return nil
}
