package main

import (
	"os"
	"path/filepath"
	"strings"
)

// extendServePath prepends the usual user and Homebrew tool directories to
// serve's PATH. A systemd user unit starts with a short PATH, and the agents,
// gh and git that PTY sessions and serve's own probes run must still resolve.
func extendServePath() {
	home, _ := os.UserHomeDir()
	extras := []string{"/opt/homebrew/bin", "/usr/local/bin"}
	if home != "" {
		extras = append(extras,
			filepath.Join(home, ".local/bin"),
			filepath.Join(home, ".linuxbrew/bin"),
			filepath.Join(home, "linuxbrew/.linuxbrew/bin"),
		)
	}
	prefix := strings.Join(extras, string(os.PathListSeparator))
	current := os.Getenv("PATH")
	if current == "" {
		_ = os.Setenv("PATH", prefix)
		return
	}
	_ = os.Setenv("PATH", prefix+string(os.PathListSeparator)+current)
}
