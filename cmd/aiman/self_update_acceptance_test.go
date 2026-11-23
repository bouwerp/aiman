package main

import (
	"slices"
	"strings"
	"testing"
)

// Acceptance tests for tui-self-update (design rev 3, piece P1).

func TestRestartArgvUsesResolvedPathAndOriginalArgs(t *testing.T) {
	got := restartArgv("/home/op/.local/bin/aiman", []string{"aiman", "--debug", "/tmp/d.log"})
	want := []string{"/home/op/.local/bin/aiman", "--debug", "/tmp/d.log"}
	if !slices.Equal(got, want) {
		t.Fatalf("restartArgv = %q, want %q", got, want)
	}
}

func TestRestartEnvSetsUpdatedFromOnce(t *testing.T) {
	env := []string{"HOME=/home/op", "AIMAN_UPDATED_FROM=v0.1.0", "TERM=xterm"}
	got := restartEnv(env, "v0.67.0")
	n := 0
	for _, kv := range got {
		if strings.HasPrefix(kv, "AIMAN_UPDATED_FROM=") {
			n++
			if kv != "AIMAN_UPDATED_FROM=v0.67.0" {
				t.Fatalf("env has %q", kv)
			}
		}
	}
	if n != 1 || !slices.Contains(got, "HOME=/home/op") || !slices.Contains(got, "TERM=xterm") {
		t.Fatalf("restartEnv = %q", got)
	}
	if env[1] != "AIMAN_UPDATED_FROM=v0.1.0" {
		t.Fatal("restartEnv changed its input")
	}
}

func TestManagedInstallRefusesPackageManagerPaths(t *testing.T) {
	for path, want := range map[string]string{
		"/opt/homebrew/Cellar/aiman/0.67.0/bin/aiman":     "Homebrew",
		"/usr/local/Cellar/aiman/0.67.0/bin/aiman":        "Homebrew",
		"/home/linuxbrew/.linuxbrew/Cellar/aiman/1/aiman": "Homebrew",
		"/nix/store/abc123-aiman-0.67.0/bin/aiman":        "nix",
	} {
		got, ok := managedInstall(path)
		if !ok || got != want {
			t.Errorf("managedInstall(%q) = %q, %v; want %q", path, got, ok, want)
		}
	}
	for _, path := range []string{"/home/op/.local/bin/aiman", "/usr/local/bin/aiman", "/Users/op/go/bin/aiman"} {
		if got, ok := managedInstall(path); ok {
			t.Errorf("managedInstall(%q) = %q, want not managed", path, got)
		}
	}
}
