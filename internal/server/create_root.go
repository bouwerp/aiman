package server

import (
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
)

// createGitRoot is the parent of `<repo>@<branch>` worktrees to use for a
// session.create on this host. The caller's worktree wins; otherwise the
// shared parent of existing scoped worktrees. An unscoped path (the main clone
// or $HOME) is ignored so we never treat `/home/code` as the registry.
func createGitRoot(caller *domain.Session, sessions []domain.Session) string {
	if caller != nil {
		if root := scopedWorktreeParent(caller.WorktreePath); root != "" {
			return root
		}
	}
	counts := map[string]int{}
	best, n := "", 0
	for _, s := range sessions {
		root := scopedWorktreeParent(s.WorktreePath)
		if root == "" {
			continue
		}
		counts[root]++
		if counts[root] > n {
			best, n = root, counts[root]
		}
	}
	return best
}

func scopedWorktreeParent(path string) string {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" || cleaned == "." || cleaned == "/" {
		return ""
	}
	if !strings.Contains(filepath.Base(cleaned), "@") {
		return ""
	}
	return filepath.Dir(cleaned)
}
