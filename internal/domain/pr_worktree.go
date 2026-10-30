package domain

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrPRWorktreeLaunch refuses to start an agent inside a pull request
// checkout. A PR's project config (agent settings, hooks, MCP servers) is
// untrusted, so review agents run in an aiman-owned scratch directory and
// read the checkout by path.
var ErrPRWorktreeLaunch = errors.New("an agent never runs in a pull request checkout")

// prWorktreeDir matches the <repo>@pr-<n> directory a review checks a PR out
// into.
var prWorktreeDir = regexp.MustCompile(`^[^/]+@pr-[0-9]+$`)

// InPRWorktree reports whether path is a <repo>@pr-<n> directory or lies
// inside one.
func InPRWorktree(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(filepath.Clean(path)), "/") {
		if prWorktreeDir.MatchString(part) {
			return true
		}
	}
	return false
}

// CheckLaunchDir refuses a directory inside a pull request checkout as an
// agent's working directory or trust root.
func CheckLaunchDir(path string) error {
	if InPRWorktree(path) {
		return fmt.Errorf("%w: %s", ErrPRWorktreeLaunch, path)
	}
	return nil
}
