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
	// DoneWhen are the repo's default completion conditions for a session.
	DoneWhen []string `yaml:"done_when,omitempty"`
	// Requires lists tools a session needs; an "mcp:" prefix names an MCP
	// server.
	Requires []string `yaml:"requires,omitempty"`
	// AgentOverrides replace agent_defaults model/effort in this repo and
	// are keyed the same way.
	AgentOverrides map[string]AgentDefaults `yaml:"agent_overrides,omitempty"`
}

// RepoBootstrapFor returns the bootstrap for an owner/name repo slug,
// matching the exact key first and then ignoring case.
func (c *Config) RepoBootstrapFor(repo string) (RepoBootstrap, bool) {
	if c == nil {
		return RepoBootstrap{}, false
	}
	return LookupRepo(c.Repos, repo)
}

// LookupRepo finds an owner/name slug in repos, matching the exact key first
// and then ignoring case.
func LookupRepo(repos map[string]RepoBootstrap, slug string) (RepoBootstrap, bool) {
	if b, ok := repos[slug]; ok {
		return b, true
	}
	for k, b := range repos {
		if strings.EqualFold(k, slug) {
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
