package ptyhold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A holder that cannot start must record why in the exit file.
//
// The holder runs detached with its stdio discarded, so a bare error return is
// invisible: meta.json is written before the socket is bound, so the manager
// had already accepted the session as started and a later failure surfaced
// only as an unexplained "exited" status with no reason anywhere. A too-long
// socket path (macOS caps them near 104 bytes) reported nothing at all.
func TestRunRecordsStartupFailureInExitFile(t *testing.T) {
	root := t.TempDir()
	dir := Dir(root, "nope")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// No request file at all: the earliest possible startup failure.
	err := Run(root, "nope")
	if err == nil {
		t.Fatal("expected Run to fail without a request file")
	}

	raw, rerr := os.ReadFile(filepath.Join(dir, ExitFile))
	if rerr != nil {
		t.Fatalf("expected the failure to be recorded in the exit file: %v", rerr)
	}
	if !strings.Contains(string(raw), "holder failed to start") {
		t.Fatalf("exit file should say the holder failed to start, got %q", raw)
	}

	// And the failure is visible through the contract, not just on disk.
	if insp := InspectSession(root, "nope"); insp.Status != StatusExited {
		t.Fatalf("status = %s, want exited", insp.Status)
	}
}

// Readiness means the live socket is bound, not merely that meta.json exists.
// The holder writes meta before binding, so a meta-only check reported success
// for sessions whose socket never came up.
func TestSocketReadyRequiresBothMetaAndSocket(t *testing.T) {
	dir := t.TempDir()
	if socketReady(dir) {
		t.Fatal("empty dir must not be ready")
	}
	if err := os.WriteFile(filepath.Join(dir, MetaFile), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if socketReady(dir) {
		t.Fatal("meta alone must not count as ready — the socket is what clients need")
	}
	if err := os.WriteFile(filepath.Join(dir, SocketFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !socketReady(dir) {
		t.Fatal("meta plus socket must be ready")
	}
}
