package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// Columns added by ALTER TABLE hold NULL on every pre-existing row. List scanned
// mode and status into plain strings, so a single such row failed the whole
// query and returned zero sessions — an empty dashboard while the database was
// full. Every other column already used sql.NullString.
func TestListToleratesNullModeAndStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aiman.db")
	repo, err := NewRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	ctx := context.Background()
	live := &domain.Session{
		ID: "live", TmuxSession: "WTB-1", RemoteHost: "regent0",
		Status: domain.SessionStatusSyncing, Mode: domain.SessionModeInteractive,
	}
	if err := repo.Save(ctx, live); err != nil {
		t.Fatal(err)
	}

	// Force the NULLs an ALTER TABLE would have left behind, on a second row.
	if _, err := repo.db.Exec(`INSERT INTO sessions (id, tmux_session, remote_host, status, mode, created_at, updated_at)
		VALUES ('legacy', 'old-session', 'regent0', NULL, NULL, datetime('now'), datetime('now'))`); err != nil {
		t.Fatal(err)
	}

	sessions, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List failed with a NULL mode/status present: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected both rows, got %d: %+v", len(sessions), sessions)
	}

	byID := map[string]domain.Session{}
	for _, s := range sessions {
		byID[s.ID] = s
	}
	if byID["live"].Status != domain.SessionStatusSyncing {
		t.Errorf("live status = %q", byID["live"].Status)
	}
	// A NULL mode reads as the interactive default rather than failing.
	if byID["legacy"].Mode != domain.SessionModeInteractive {
		t.Errorf("legacy mode = %q, want the interactive default", byID["legacy"].Mode)
	}

	// Get must tolerate it too.
	got, err := repo.Get(ctx, "legacy")
	if err != nil {
		t.Fatalf("Get failed on a NULL mode/status row: %v", err)
	}
	if got.TmuxSession != "old-session" {
		t.Errorf("Get returned %+v", got)
	}
}

// Opening the database normalises the NULLs away, so stored data stops carrying
// them even though the scans now cope.
func TestOpenNormalisesNullModeAndStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aiman.db")
	repo, err := NewRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(`INSERT INTO sessions (id, tmux_session, status, mode, created_at, updated_at)
		VALUES ('legacy', 'old', NULL, NULL, datetime('now'), datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	repo.Close()

	reopened, err := NewRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	var nulls int
	if err := reopened.db.QueryRow("SELECT count(*) FROM sessions WHERE mode IS NULL OR status IS NULL").Scan(&nulls); err != nil {
		t.Fatal(err)
	}
	if nulls != 0 {
		t.Errorf("expected reopening to normalise NULLs, %d remain", nulls)
	}
}
