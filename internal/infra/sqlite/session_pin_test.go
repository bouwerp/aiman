package sqlite

import (
	"context"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestSetPinnedSurvivesSaveAndClears(t *testing.T) {
	r, log := changeRepo(t)
	ctx := context.Background()
	log.reset()
	if err := r.SetPinned(ctx, "s1", true); err != nil {
		t.Fatal(err)
	}
	if got := log.wait(); !hasKind(got, ChangeIdentity) {
		t.Fatalf("SetPinned reported %v, want ChangeIdentity", got)
	}
	// A discovery save knows nothing of the pin.
	if err := r.Save(ctx, &domain.Session{ID: "s1", Status: domain.SessionStatusActive}); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Pinned {
		t.Fatal("Save cleared the pin")
	}
	list, err := r.List(ctx)
	if err != nil || len(list) != 1 || !list[0].Pinned {
		t.Fatalf("List = %+v, %v; want the pinned row", list, err)
	}
	if err := r.SetPinned(ctx, "s1", false); err != nil {
		t.Fatal(err)
	}
	// Save never writes the pin, so a pinned value in memory does not pin the row.
	pinned := changeBase("s1")
	pinned.Pinned = true
	if err := r.Save(ctx, pinned); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Get(ctx, "s1"); got.Pinned {
		t.Fatal("Save wrote the pin")
	}
}
