package ptyhold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// The spawn wait must answer before the agent API client gives up.
//
// A pty.create request reaches Spawn through internal/server's client, which
// sets a 30s read deadline. Setting this wait to 30s too meant that on a slow
// runner the client timed out at the exact moment the server was still
// waiting, surfacing "i/o timeout" instead of the real result.
func TestSpawnReadyTimeoutLeavesHeadroomForTheAPIClient(t *testing.T) {
	const apiClientDeadline = 30 * time.Second
	if spawnReadyTimeout >= apiClientDeadline {
		t.Fatalf("spawnReadyTimeout (%s) must be below the API client deadline (%s)",
			spawnReadyTimeout, apiClientDeadline)
	}
	// And comfortably below, not merely under by a hair.
	if spawnReadyTimeout > apiClientDeadline/2 {
		t.Errorf("spawnReadyTimeout (%s) leaves too little headroom under %s",
			spawnReadyTimeout, apiClientDeadline)
	}
}

// A holder that dies before it can write the exit file must still explain
// itself. Its stdio used to be discarded, so the only signal was "did not
// become ready in time" — indistinguishable between a slow spawn, a binary
// that cannot exec, and a PTY that cannot be allocated.
func TestSpawnTimeoutQuotesHolderOutput(t *testing.T) {
	root := t.TempDir()
	// A "holder" that complains and exits without touching the contract files.
	fake := []string{"sh", "-c", `echo "cannot allocate pty: no such device" >&2; exit 3`, "--"}

	err := Spawn(root, Spec{ID: "diag", Command: "true"}, fake)
	if err == nil {
		t.Fatal("expected Spawn to fail")
	}
	if !strings.Contains(err.Error(), "cannot allocate pty") {
		t.Fatalf("error must quote what the holder printed, got: %v", err)
	}
}
