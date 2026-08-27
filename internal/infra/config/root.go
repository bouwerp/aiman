package config

import (
	"os"
	"path/filepath"
	"strings"
)

// ServeGitRoot is the directory aiman serve treats as the git worktree parent
// when creating sessions on this host.
//
// Agent API config has no remotes[] (those describe the TUI's SSH targets), so
// the root has to come from git.root, $HOME/repos when that directory exists,
// or $HOME as last resort.
func ServeGitRoot(c *Config, home string, dirExists func(string) bool) string {
	if c != nil {
		if root := strings.TrimRight(strings.TrimSpace(c.Git.Root), "/"); root != "" {
			return root
		}
		if len(c.Remotes) > 0 {
			if root := strings.TrimRight(strings.TrimSpace(c.Remotes[0].Root), "/"); root != "" {
				return root
			}
		}
	}
	home = strings.TrimRight(strings.TrimSpace(home), "/")
	if home == "" {
		return ""
	}
	candidate := filepath.Join(home, "repos")
	if dirExists != nil && dirExists(candidate) {
		return candidate
	}
	return home
}

// DirExists reports whether path is a directory. Passed to ServeGitRoot.
func DirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
