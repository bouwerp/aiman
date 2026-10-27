package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

// RepoBootstrap prepares a fresh worktree of one repository. Copy and
// Symlink paths are relative to the repository root and are taken from the
// main checkout; Setup runs in the worktree in the background.
type RepoBootstrap struct {
	Copy    []string `yaml:"copy,omitempty"`
	Symlink []string `yaml:"symlink,omitempty"`
	Setup   string   `yaml:"setup,omitempty"`
}

// RepoBootstrapFor returns the bootstrap for an owner/name repo slug,
// matching the exact key first and then ignoring case.
func (c *Config) RepoBootstrapFor(repo string) (RepoBootstrap, bool) {
	if c == nil {
		return RepoBootstrap{}, false
	}
	if b, ok := c.Repos[repo]; ok {
		return b, true
	}
	for k, b := range c.Repos {
		if strings.EqualFold(k, repo) {
			return b, true
		}
	}
	return RepoBootstrap{}, false
}

// Validate rejects Copy and Symlink entries that are empty, absolute, name
// the repository root, or escape it with "..".
func (b RepoBootstrap) Validate() error {
	for _, set := range []struct {
		kind  string
		paths []string
	}{{"copy", b.Copy}, {"symlink", b.Symlink}} {
		for _, p := range set.paths {
			if err := validateRepoRelPath(p); err != nil {
				return fmt.Errorf("repos %s entry %q: %w", set.kind, p, err)
			}
		}
	}
	return nil
}

func validateRepoRelPath(p string) error {
	if strings.TrimSpace(p) == "" {
		return fmt.Errorf("path is empty")
	}
	if filepath.IsAbs(p) {
		return fmt.Errorf("path must be relative")
	}
	clean := filepath.Clean(p)
	if clean == "." {
		return fmt.Errorf("path names the repository root")
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes the repository")
	}
	return nil
}
