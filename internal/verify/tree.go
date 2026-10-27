package verify

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TreeHash is the tree TakeSnapshot would commit for worktree: tracked,
// modified and untracked files through a temporary index. It writes no
// commit, so the before-PR gate can match a review cheaply.
func TreeHash(ctx context.Context, worktree string) (string, error) {
	tmp, err := os.MkdirTemp("", "aiman-review-index-")
	if err != nil {
		return "", fmt.Errorf("create snapshot index dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	env := append(os.Environ(), "GIT_INDEX_FILE="+filepath.Join(tmp, "index"))
	if _, err := runGit(ctx, worktree, env, "read-tree", "HEAD"); err != nil {
		return "", err
	}
	if _, err := runGit(ctx, worktree, env, "add", "-A"); err != nil {
		return "", err
	}
	tree, err := runGit(ctx, worktree, env, "write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(tree), nil
}
