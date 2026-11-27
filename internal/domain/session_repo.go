package domain

import (
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
