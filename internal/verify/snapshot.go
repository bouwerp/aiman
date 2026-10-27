package verify

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// MaxPatchBytes caps the patch carried into a reviewer brief.
const MaxPatchBytes = 150 * 1024

// Snapshot is the commit and tree produced from a worker's working tree.
type Snapshot struct {
	Commit string
	Tree   string
}

// Diff is the review diff between the merge base and the snapshot.
type Diff struct {
	Stat      string
	Patch     string
	Truncated bool
	MergeBase string
}

// TakeSnapshot commits the worktree's tracked, modified and untracked files
// through a temporary index, leaving the real index and status untouched.
func TakeSnapshot(ctx context.Context, worktree string) (Snapshot, error) {
	tmp, err := os.MkdirTemp("", "aiman-review-index-")
	if err != nil {
		return Snapshot{}, fmt.Errorf("create snapshot index dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	index := filepath.Join(tmp, "index")
	env := append(os.Environ(), "GIT_INDEX_FILE="+index)

	if _, err := runGit(ctx, worktree, env, "read-tree", "HEAD"); err != nil {
		return Snapshot{}, err
	}
	if _, err := runGit(ctx, worktree, env, "add", "-A"); err != nil {
		return Snapshot{}, err
	}
	tree, err := runGit(ctx, worktree, env, "write-tree")
	if err != nil {
		return Snapshot{}, err
	}
	tree = strings.TrimSpace(tree)
	commit, err := runGit(ctx, worktree, nil, "commit-tree", tree, "-p", "HEAD", "-m", "aiman review snapshot")
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Commit: strings.TrimSpace(commit), Tree: tree}, nil
}

// AddDetachedWorktree checks out commit at path without attaching a branch.
func AddDetachedWorktree(ctx context.Context, repoWorktree, path, commit string) error {
	_, err := runGit(ctx, repoWorktree, nil, "worktree", "add", "--detach", path, commit)
	return err
}

// RemoveWorktree removes a review worktree and prunes its metadata.
func RemoveWorktree(ctx context.Context, repoWorktree, path string) error {
	if _, err := os.Stat(path); err == nil {
		if _, err := runGit(ctx, repoWorktree, nil, "worktree", "remove", "--force", path); err != nil {
			return err
		}
	}
	_, err := runGit(ctx, repoWorktree, nil, "worktree", "prune")
	return err
}

// ResolveBase resolves the diff base. Empty uses origin's default branch, then
// origin/main and origin/master; a named base falls back to origin/<base>.
func ResolveBase(ctx context.Context, worktree, base string) (string, error) {
	base = strings.TrimSpace(base)
	if base != "" {
		if ref, ok := resolveCommit(ctx, worktree, base); ok {
			return ref, nil
		}
		if !strings.HasPrefix(base, "origin/") {
			if ref, ok := resolveCommit(ctx, worktree, "origin/"+base); ok {
				return ref, nil
			}
		}
		return "", fmt.Errorf("cannot resolve base %q", base)
	}

	if out, err := runGit(ctx, worktree, nil, "rev-parse", "--abbrev-ref", "refs/remotes/origin/HEAD"); err == nil {
		ref := strings.TrimSpace(out)
		if ref != "" {
			if resolved, ok := resolveCommit(ctx, worktree, ref); ok {
				return resolved, nil
			}
		}
	}
	for _, ref := range []string{"origin/main", "origin/master"} {
		if resolved, ok := resolveCommit(ctx, worktree, ref); ok {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("cannot resolve the origin default branch")
}

// DiffAgainst diffs commit against baseRef from their merge base.
func DiffAgainst(ctx context.Context, worktree, baseRef, commit string, maxPatchBytes int) (Diff, error) {
	mergeBase, err := runGit(ctx, worktree, nil, "merge-base", baseRef, commit)
	if err != nil {
		return Diff{}, err
	}
	mergeBase = strings.TrimSpace(mergeBase)
	stat, err := runGit(ctx, worktree, nil, "diff", "--stat", mergeBase, commit)
	if err != nil {
		return Diff{}, err
	}
	patch, err := runGit(ctx, worktree, nil, "diff", mergeBase, commit)
	if err != nil {
		return Diff{}, err
	}
	out := Diff{Stat: strings.TrimSpace(stat), Patch: patch, MergeBase: mergeBase}
	if maxPatchBytes > 0 && len(patch) > maxPatchBytes {
		out.Patch = patch[:maxPatchBytes]
		out.Truncated = true
	}
	return out, nil
}

func resolveCommit(ctx context.Context, worktree, ref string) (string, bool) {
	out, err := runGit(ctx, worktree, nil, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(out), true
}

func runGit(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	// #nosec G204 -- the verb is fixed and every path is passed as an argv element.
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	if env != nil {
		cmd.Env = env
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return string(out), nil
}
