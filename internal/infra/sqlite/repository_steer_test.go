package sqlite

import (
	"context"
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
