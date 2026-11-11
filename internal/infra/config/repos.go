package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
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
	// Review is per-repo review sandbox settings.
	Review RepoReview `yaml:"review,omitempty"`
	// CI is the repo's local CI gate settings.
	CI RepoCI `yaml:"ci,omitempty"`
}

// RepoCI is repos.<slug>.ci. Local, when set, replaces the repository's own
// .aiman/ci-local.yaml. DraftsSkipCI says the repo's workflows skip draft PRs,
// so aiman treats a draft PR as free.
type RepoCI struct {
	DraftsSkipCI bool            `yaml:"drafts_skip_ci,omitempty"`
	Local        []RepoCICommand `yaml:"local,omitempty"`
}

// RepoCICommand is one local gate command. Dir is relative to the repository
// root; Timeout is a duration string, 45m when empty.
type RepoCICommand struct {
	Name    string `yaml:"name"`
	Run     string `yaml:"run"`
	Dir     string `yaml:"dir,omitempty"`
	Timeout string `yaml:"timeout,omitempty"`
}

// RepoReview is repos.<slug>.review: the sandbox image override and the
// command that writes Istanbul coverage-final.json for a non-Go tree.
type RepoReview struct {
	Image    string `yaml:"image,omitempty"`
	Coverage string `yaml:"coverage,omitempty"`
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
// the repository root, or escape it with "..", and ci.local entries without a
// name or run, or whose dir is absolute or escapes the repository.
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
	return b.CI.validate()
}

func (c RepoCI) validate() error {
	for i, cmd := range c.Local {
		if strings.TrimSpace(cmd.Name) == "" {
			return fmt.Errorf("repos ci.local entry %d: name is required", i+1)
		}
		if strings.TrimSpace(cmd.Run) == "" {
			return fmt.Errorf("repos ci.local entry %q: run is required", cmd.Name)
		}
		if d := strings.TrimSpace(cmd.Dir); d != "" && filepath.Clean(d) != "." {
			if err := validateRepoRelPath(d); err != nil {
				return fmt.Errorf("repos ci.local entry %q dir %q: %w", cmd.Name, d, err)
			}
		}
		if t := strings.TrimSpace(cmd.Timeout); t != "" {
			if d, err := time.ParseDuration(t); err != nil || d <= 0 {
				return fmt.Errorf("repos ci.local entry %q timeout %q is not a positive duration", cmd.Name, t)
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
