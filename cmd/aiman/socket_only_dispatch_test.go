package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/config"
)

// runWithArgs invokes the real entry point with a sandboxed home.
func runWithArgs(t *testing.T, home string, args ...string) error {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // windows
	saved := os.Args
	t.Cleanup(func() { os.Args = saved })
	os.Args = append([]string{"aiman"}, args...)
	return run()
}

// TestSocketOnlyCommandsSkipTheDatabase pins that commands which only talk to
// the agent API socket do not pay for the TUI's setup — loading config, opening
// SQLite and running its migrations, building the JIRA/git/SSH/flow plumbing.
// That cost about 48ms per call, and the dashboard makes these calls twice a
// second while previewing a PTY session.
//
// The database file is the tell: if it appears, the setup ran.
func TestSocketOnlyCommandsSkipTheDatabase(t *testing.T) {
	for _, args := range [][]string{
		{"pty", "list"},
		{"pty", "capture", "nope"},
		{"session", "list"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			home := t.TempDir()
			// These fail (no serve is running); the point is what they touch.
			_ = runWithArgs(t, home, args...)

			if _, err := os.Stat(filepath.Join(home, ".aiman", config.DBName)); err == nil {
				t.Fatalf("%v opened the database; it only needs the socket", args)
			}
		})
	}
}

// The premise of the test above: a command that does go through the setup
// really does create the database, so the assertion can tell them apart.
func TestNormalCommandsDoOpenTheDatabase(t *testing.T) {
	home := t.TempDir()
	if err := runWithArgs(t, home, "--version"); err != nil {
		t.Fatalf("--version: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".aiman", config.DBName)); err != nil {
		t.Fatalf("expected the database to exist after a normal command: %v", err)
	}
}

// The holder needs neither the socket nor a resolvable home, so it must reach
// its own argument validation even when both are unusable. Opening the shared
// database while serve holds it returns "database is locked", and a holder that
// cannot start is a session that cannot start.
func TestPTYHoldReachesItsOwnValidation(t *testing.T) {
	brokenHome := filepath.Join(t.TempDir(), "home-is-a-file")
	if err := os.WriteFile(brokenHome, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runWithArgs(t, brokenHome, "pty", "hold")
	if err == nil {
		t.Fatal("expected the holder's own usage error")
	}
	if !strings.Contains(err.Error(), "requires --root and --id") {
		t.Fatalf("holder should fail on its own arguments, not on setup: %v", err)
	}
}
