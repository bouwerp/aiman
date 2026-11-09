package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestSteerNotesRoundTrip(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)

	notes := []*domain.SteerRecord{
		{SessionID: "s1", Author: domain.AuthorUser, Text: "a", CreatedAt: base},
		{SessionID: "s1", Author: domain.AuthorAiman, Text: "b", CreatedAt: base.Add(time.Second)},
		{SessionID: "s2", Author: domain.AuthorUser, Text: "c", CreatedAt: base.Add(2 * time.Second)},
	}
	for _, n := range notes {
		if err := repo.AddSteerNote(ctx, n); err != nil {
			t.Fatalf("AddSteerNote: %v", err)
		}
		if n.ID == 0 {
			t.Fatal("AddSteerNote did not set ID")
		}
	}

	at := base.Add(time.Minute)
	if err := repo.MarkSteerNotesDelivered(ctx, []int64{notes[0].ID}, at); err != nil {
		t.Fatalf("MarkSteerNotesDelivered: %v", err)
	}
	got, err := repo.ListUndeliveredSteerNotes(ctx)
	if err != nil {
		t.Fatalf("ListUndeliveredSteerNotes: %v", err)
	}
	if len(got) != 2 || got[0].Text != "b" || got[1].Text != "c" || got[0].Author != domain.AuthorAiman {
		t.Fatalf("undelivered = %+v", got)
	}
	if !got[0].CreatedAt.Equal(notes[1].CreatedAt) {
		t.Fatalf("CreatedAt = %v want %v", got[0].CreatedAt, notes[1].CreatedAt)
	}

	if err := repo.DeleteSteerNotes(ctx, "s2"); err != nil {
		t.Fatalf("DeleteSteerNotes: %v", err)
	}
	got, _ = repo.ListUndeliveredSteerNotes(ctx)
	if len(got) != 1 || got[0].SessionID != "s1" {
		t.Fatalf("after delete = %+v", got)
	}

	last, err := repo.LastSteerDelivery(ctx)
	if err != nil {
		t.Fatalf("LastSteerDelivery: %v", err)
	}
	if !last["s1"].Equal(at) {
		t.Fatalf("s1 last = %v want %v", last["s1"], at)
	}
	if _, ok := last["s2"]; ok || len(last) != 1 {
		t.Fatalf("last = %v", last)
	}
}

func TestSteerNoteFromRoundTrips(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	now := time.Now()
	peer := domain.SteerRecord{SessionID: "b", Author: domain.AuthorPeer, From: "a", Text: "[peer session a a] hi", CreatedAt: now}
	user := domain.SteerRecord{SessionID: "b", Author: domain.AuthorUser, Text: "rebase", CreatedAt: now}
	for _, r := range []*domain.SteerRecord{&peer, &user} {
		if err := repo.AddSteerNote(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := repo.ListUndeliveredSteerNotes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].From != "a" || got[0].Author != domain.AuthorPeer || got[1].From != "" {
		t.Fatalf("notes = %+v, want the peer note from a and the user note with none", got)
	}
}

// TestSteerNotesFromMigration opens a database whose steer_notes predates the
// from column: the column is added and existing rows read with From empty.
func TestSteerNotesFromMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aiman.db")
	old, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE steer_notes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL,
		author TEXT NOT NULL,
		text TEXT NOT NULL,
		created_at DATETIME NOT NULL,
		delivered_at DATETIME
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO steer_notes (session_id, author, text, created_at) VALUES ('s1', 'user', 'old note', ?)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	repo, err := NewRepository(path)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	t.Cleanup(func() { repo.Close() })
	var n int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('steer_notes') WHERE name = 'from'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("from column count = %d (%v), want 1", n, err)
	}
	got, err := repo.ListUndeliveredSteerNotes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "old note" || got[0].From != "" {
		t.Fatalf("notes = %+v, want the old note with From empty", got)
	}
}
