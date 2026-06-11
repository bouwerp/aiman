package ui

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestIsHealthySyncStatus(t *testing.T) {
	healthy := []string{
		"Watching for changes",
		"Scanning files",
		"Staging files on beta",
		"Reconciling changes",
		"Applying changes",
		"Saving archive",
		"Watching for changes [Conflicts]",
	}
	for _, s := range healthy {
		if !isHealthySyncStatus(s) {
			t.Errorf("expected %q to be healthy", s)
		}
	}

	stale := []string{
		"",
		"Halted due to root deletion",
		"Waiting 5 seconds to retry",
		"Paused",
		"Connecting to beta",
		"Errored",
	}
	for _, s := range stale {
		if isHealthySyncStatus(s) {
			t.Errorf("expected %q to be unhealthy", s)
		}
	}
}

func TestClassifySyncHealth_MissingSync(t *testing.T) {
	s := domain.Session{ID: "abc-123", MutagenSyncID: "aiman-sync-abc-123"}
	h := classifySyncHealth(s, nil, "devbox")
	if !h.stale {
		t.Fatal("expected missing sync to be stale")
	}
	if h.found {
		t.Fatal("expected found=false for missing sync")
	}
}

func TestClassifySyncHealth_HealthyByName(t *testing.T) {
	s := domain.Session{ID: "abc-123", MutagenSyncID: "aiman-sync-abc-123"}
	syncs := []domain.SyncSession{{
		Name:           "aiman-sync-abc-123",
		Status:         "Watching for changes",
		RemoteEndpoint: "code@devbox",
	}}
	h := classifySyncHealth(s, syncs, "devbox")
	if h.stale {
		t.Fatalf("expected healthy sync, got stale (%s)", h.reason)
	}
	if !h.found {
		t.Fatal("expected found=true")
	}
}

func TestClassifySyncHealth_MatchByLabel(t *testing.T) {
	s := domain.Session{ID: "abc-123", MutagenSyncID: "some-old-uuid"}
	syncs := []domain.SyncSession{{
		Name:           "legacy-name",
		Status:         "Watching for changes",
		RemoteEndpoint: "code@devbox",
		Labels:         map[string]string{"aiman-id": "abc-123"},
	}}
	h := classifySyncHealth(s, syncs, "devbox")
	if h.stale {
		t.Fatalf("expected label-matched sync to be healthy, got stale (%s)", h.reason)
	}
}

func TestClassifySyncHealth_StaleStatus(t *testing.T) {
	s := domain.Session{ID: "abc-123", MutagenSyncID: "aiman-sync-abc-123"}
	syncs := []domain.SyncSession{{
		Name:           "aiman-sync-abc-123",
		Status:         "Halted due to root deletion",
		RemoteEndpoint: "code@devbox",
	}}
	h := classifySyncHealth(s, syncs, "devbox")
	if !h.stale {
		t.Fatal("expected halted sync to be stale")
	}
	if h.reason != "Halted due to root deletion" {
		t.Errorf("unexpected reason: %s", h.reason)
	}
}

func TestClassifySyncHealth_HostMismatch(t *testing.T) {
	// Session's remote moved (e.g. instance replaced) but the sync still
	// points at the old host: must be flagged stale even if mutagen reports
	// a nominally healthy status.
	s := domain.Session{ID: "abc-123", MutagenSyncID: "aiman-sync-abc-123"}
	syncs := []domain.SyncSession{{
		Name:           "aiman-sync-abc-123",
		Status:         "Watching for changes",
		RemoteEndpoint: "code@old-host",
	}}
	h := classifySyncHealth(s, syncs, "new-host")
	if !h.stale {
		t.Fatal("expected host-mismatched sync to be stale")
	}
}

func TestEndpointHost(t *testing.T) {
	if got := endpointHost("code@devbox"); got != "devbox" {
		t.Errorf("endpointHost(code@devbox) = %q", got)
	}
	if got := endpointHost("devbox"); got != "devbox" {
		t.Errorf("endpointHost(devbox) = %q", got)
	}
}
