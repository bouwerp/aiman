package config

import "testing"

func TestServeGitRootPrefersExplicitGitRoot(t *testing.T) {
	cfg := &Config{
		Git:     GitConfig{Root: "/srv/worktrees"},
		Remotes: []Remote{{Root: "/from-remote"}},
	}
	got := ServeGitRoot(cfg, "/home/code", func(string) bool { return true })
	if got != "/srv/worktrees" {
		t.Fatalf("got %q, want the configured git.root", got)
	}
}

func TestServeGitRootUsesRemoteRootWhenGitRootUnset(t *testing.T) {
	cfg := &Config{Remotes: []Remote{{Root: "/home/code/repos"}}}
	got := ServeGitRoot(cfg, "/home/code", func(string) bool { return true })
	if got != "/home/code/repos" {
		t.Fatalf("got %q, want the first remote root", got)
	}
}

func TestServeGitRootPrefersHomeReposOverBareHome(t *testing.T) {
	exists := func(p string) bool { return p == "/home/code/repos" }
	got := ServeGitRoot(&Config{}, "/home/code", exists)
	if got != "/home/code/repos" {
		t.Fatalf("got %q, want $HOME/repos when that directory exists", got)
	}
}

func TestServeGitRootFallsBackToHomeWhenReposMissing(t *testing.T) {
	got := ServeGitRoot(&Config{}, "/home/code", func(string) bool { return false })
	if got != "/home/code" {
		t.Fatalf("got %q, want $HOME when $HOME/repos does not exist", got)
	}
}
