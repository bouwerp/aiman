package contextstore

import (
	"context"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestIndexedFindMatchesScan(t *testing.T) {
	scan := NewFiles(t.TempDir())
	seedNotes(t, scan, 200)
	indexed := NewFiles(scan.root)
	indexed.AttachIndex(newTestIndex(t))
	if _, _, err := indexed.Reindex(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"auth", "cookie", "deploy", "parser", "review"} {
		want, err := scan.Find(context.Background(), domain.ContextQuery{Text: text})
		if err != nil {
			t.Fatal(err)
		}
		got, err := indexed.Find(context.Background(), domain.ContextQuery{Text: text})
		if err != nil {
			t.Fatal(err)
		}
		wantIDs, gotIDs := indexIDs(want), indexIDs(got)
		sort.Strings(wantIDs)
		sort.Strings(gotIDs)
		if len(wantIDs) != len(gotIDs) {
			t.Fatalf("%q: scan %v, index %v", text, wantIDs, gotIDs)
		}
		for i := range wantIDs {
			if wantIDs[i] != gotIDs[i] {
				t.Fatalf("%q: scan %v, index %v", text, wantIDs, gotIDs)
			}
		}
	}
}

func TestPutUpdatesIndex(t *testing.T) {
	f := NewFiles(t.TempDir())
	ix := newTestIndex(t)
	f.AttachIndex(ix)
	ctx := context.Background()
	if _, err := f.Put(ctx, domain.ContextEntry{Title: "Fresh", Body: "needle", Namespace: domain.ContextNSGroup, Key: "G1"}); err != nil {
		t.Fatal(err)
	}
	list, err := ix.Search(ctx, domain.ContextQuery{Text: "needle"}, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Title != "Fresh" {
		t.Fatalf("index = %+v", list)
	}
}

func TestReindexPicksUpOfflineEdits(t *testing.T) {
	f := NewFiles(t.TempDir())
	edited := putRankNote(t, f, domain.ContextEntry{Title: "Before", Body: "needle"})
	deleted := putRankNote(t, f, domain.ContextEntry{Title: "Delete me", Body: "needle"})
	ix := newTestIndex(t)
	f.AttachIndex(ix)
	if _, _, err := f.Reindex(context.Background()); err != nil {
		t.Fatal(err)
	}

	offline := NewFiles(f.root)
	if _, err := offline.Put(context.Background(), domain.ContextEntry{
		ID: edited.ID, Namespace: edited.Namespace, Key: edited.Key, Title: "After", Body: "needle",
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(absPath(f.root, deleted)); err != nil {
		t.Fatal(err)
	}

	reopened := NewFiles(f.root)
	reopened.AttachIndex(ix)
	updated, removed, err := reopened.Reindex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if updated != 1 || removed != 1 {
		t.Fatalf("updated=%d removed=%d", updated, removed)
	}
	list, err := reopened.Find(context.Background(), domain.ContextQuery{Text: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	got := titles(list)
	if len(got) != 1 || got[0] != "After" {
		t.Fatalf("find = %v", got)
	}
}

func TestReindexSkipsUnchanged(t *testing.T) {
	f := NewFiles(t.TempDir())
	putRankNote(t, f, domain.ContextEntry{Title: "One", Body: "needle"})
	f.AttachIndex(newTestIndex(t))
	if _, _, err := f.Reindex(context.Background()); err != nil {
		t.Fatal(err)
	}
	updated, removed, err := f.Reindex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if updated != 0 || removed != 0 {
		t.Fatalf("updated=%d removed=%d", updated, removed)
	}
}

func TestBrokenIndexFallsBackToScan(t *testing.T) {
	f := NewFiles(t.TempDir())
	putRankNote(t, f, domain.ContextEntry{Title: "Fallback", Body: "needle"})
	ix := newTestIndex(t)
	f.AttachIndex(ix)
	if err := ix.db.Close(); err != nil {
		t.Fatal(err)
	}
	list, err := f.Find(context.Background(), domain.ContextQuery{Text: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Title != "Fallback" {
		t.Fatalf("fallback find = %+v", list)
	}
}

func TestGetUsesIndexedPath(t *testing.T) {
	f := NewFiles(t.TempDir())
	note := putRankNote(t, f, domain.ContextEntry{Title: "Body note", Body: "the body"})
	f.AttachIndex(newTestIndex(t))
	if _, _, err := f.Reindex(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := f.Get(context.Background(), note.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != "the body" {
		t.Fatalf("body = %q", got.Body)
	}
}
