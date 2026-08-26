package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPTYHoldDispatchesBeforeConfigAndDatabase pins that the holder does not
// pay for — or depend on — the config load, SQLite open, and SSH/JIRA setup that
// every other command needs.
//
// It only ever gets an explicit --root and --id. Going through that setup made
// every session start open the shared database, which fails outright with
// "database is locked" whenever serve or the dashboard happens to hold it — and
// a holder that cannot start is a session that cannot start.
func TestPTYHoldDispatchesBeforeConfigAndDatabase(t *testing.T) {
	// A HOME that is a regular file makes the config/database setup fail, so the
	// two paths are told apart by which error comes back.
	brokenHome := filepath.Join(t.TempDir(), "home-is-a-file")
	if err := os.WriteFile(brokenHome, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", brokenHome)
	t.Setenv("USERPROFILE", brokenHome) // windows

	saved := os.Args
	t.Cleanup(func() { os.Args = saved })

	// Premise: any other command trips over the unusable HOME during setup.
	os.Args = []string{"aiman", "--version"}
	err := run()
	if err == nil {
		t.Fatal("expected the broken HOME to fail the normal setup path")
	}
	if !strings.Contains(err.Error(), "config directory") && !strings.Contains(err.Error(), "database") {
		t.Fatalf("expected a config/database failure, got %v", err)
	}

	// The holder reaches its own argument validation instead.
	os.Args = []string{"aiman", "pty", "hold"}
	err = run()
	if err == nil {
		t.Fatal("expected the holder's own usage error")
	}
	if !strings.Contains(err.Error(), "requires --root and --id") {
		t.Fatalf("holder should fail on its own arguments, not on setup: %v", err)
	}
}
