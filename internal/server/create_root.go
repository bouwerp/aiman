package server

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
)

// createGitRoot is the parent of `<repo>@<branch>` worktrees to use for a
// session.create on this host. The caller's worktree wins; otherwise the
// shared parent of existing scoped worktrees. An unscoped path (the main clone
// or $HOME) is ignored so we never treat `/home/code` as the registry.
//
// home is this host's home directory. A worktree sitting directly in $HOME is
// never evidence of a registry: agents create sessions from inside other
// sessions, so a single worktree that landed beside the main clones would
// otherwise be inherited by every session created from it, and by every
// session created from those in turn. Skipping it lets the answer fall back to
// the configured serve git root ($HOME/repos), which is where worktrees belong.
func createGitRoot(caller *domain.Session, sessions []domain.Session, home string) string {
	home = strings.TrimRight(strings.TrimSpace(home), "/")
	if caller != nil {
		if root := scopedWorktreeParent(caller.WorktreePath); root != "" && root != home {
			return root
		}
	}
	counts := map[string]int{}
	best, n := "", 0
	for _, s := range sessions {
		root := scopedWorktreeParent(s.WorktreePath)
		if root == "" || root == home {
			continue
		}
		counts[root]++
		if counts[root] > n {
			best, n = root, counts[root]
		}
	}
	return best
}

// hostHome is the home directory createGitRoot measures candidates against. An
// unreadable home yields "", which disables the check rather than guessing.
func hostHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
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
