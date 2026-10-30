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
// it fetches the head itself and is reset hard to it, which is how a review
// refresh restages a moved head.
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
	// Before ensureHealthyRepo fetches anything, refuse a clone of another
	// repository. A clone whose origin cannot be read yet is left to its
	// recovery, and checked strictly once that is done.
	if origin, err := readOrigin(ctx, remote, repoPath); err == nil {
		if err := matchOrigin(origin, repoPath, repo.Name); err != nil {
			return domain.Worktree{}, err
		}
	}
	if err := ensureHealthyRepo(ctx, remote, repoPath, repo.URL); err != nil {
		return domain.Worktree{}, err
	}
	origin, err := readOrigin(ctx, remote, repoPath)
	if err != nil {
		return domain.Worktree{}, fmt.Errorf("read the origin of %s: %w", repoPath, err)
	}
	if err := matchOrigin(origin, repoPath, repo.Name); err != nil {
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

// readOrigin returns the clone's configured origin URL, as written in its
// config rather than after url.<base>.insteadOf rewriting.
func readOrigin(ctx context.Context, remote domain.RemoteExecutor, repoPath string) (string, error) {
	out, err := remote.Execute(ctx, "git -C "+shellQuote(repoPath)+" config --get remote.origin.url")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// matchOrigin refuses an origin that is not github.com/<slug>: a PR number
// means nothing in another repository.
func matchOrigin(origin, repoPath, slug string) error {
	if !originNamesRepo(origin, slug) {
		return fmt.Errorf("%w: the clone at %s has origin %q, not github.com/%s", domain.ErrOriginMismatch, repoPath, origin, slug)
	}
	return nil
}

// originNamesRepo reports whether a git remote URL is github.com/<slug>:
// https or ssh:// (optional user and port) or scp-like git@github.com:owner/repo,
// ignoring case, a .git suffix and a trailing slash. Any other host, a local
// path or a file URL is not.
func originNamesRepo(origin, slug string) bool {
	host, path, ok := splitRemoteURL(strings.ToLower(strings.TrimSpace(origin)))
	if !ok || host != "github.com" {
		return false
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	return path == strings.ToLower(slug)
}

// splitRemoteURL splits a scheme URL or an scp-like address into its host
// (without user or port) and path.
func splitRemoteURL(u string) (host, path string, ok bool) {
	if scheme, rest, found := strings.Cut(u, "://"); found {
		if scheme != "https" && scheme != "http" && scheme != "ssh" && scheme != "git+ssh" {
			return "", "", false
		}
		authority, p, _ := strings.Cut(rest, "/")
		if i := strings.LastIndex(authority, "@"); i >= 0 {
			authority = authority[i+1:]
		}
		h, _, _ := strings.Cut(authority, ":")
		return h, p, h != ""
	}
	if strings.HasPrefix(u, "/") || strings.HasPrefix(u, ".") {
		return "", "", false
	}
	authority, p, found := strings.Cut(u, ":")
	if !found || strings.Contains(authority, "/") {
		return "", "", false
	}
	if i := strings.LastIndex(authority, "@"); i >= 0 {
		authority = authority[i+1:]
	}
	return authority, p, authority != ""
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
		// The worktree is detached, so the hard reset moves no branch; it drops
		// the files staged from the previous head before the new soft reset.
		if out, err := remote.Execute(ctx, "git -C "+wt+" checkout -q --detach && git -C "+wt+" reset -q --hard FETCH_HEAD 2>&1"); err != nil {
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
