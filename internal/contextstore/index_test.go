package contextstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"sort"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/bouwerp/aiman/internal/domain"
)

func newTestIndex(t *testing.T) *Index {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ix, err := OpenIndex(db)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func putIndexEntry(t *testing.T, ix *Index, e domain.ContextEntry) {
	t.Helper()
	if e.ID == "" {
		t.Fatal("id is required")
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	if err := ix.Upsert(context.Background(), e, filepath.Join("/notes", e.ID+".md"), e.CreatedAt); err != nil {
		t.Fatal(err)
	}
}

func indexIDs(list []domain.ContextEntry) []string {
	out := make([]string, 0, len(list))
	for _, e := range list {
		out = append(out, e.ID)
	}
	return out
}

func TestIndexSearchMatchesPrefixTokens(t *testing.T) {
	ix := newTestIndex(t)
	putIndexEntry(t, ix, domain.ContextEntry{ID: "s1", Title: "Auth", Body: "Session cookie auth"})
	putIndexEntry(t, ix, domain.ContextEntry{ID: "s2", Title: "API", Body: "cookie api"})
	list, err := ix.Search(context.Background(), domain.ContextQuery{Text: "sess cook"}, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := indexIDs(list); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("prefix search = %v, want s1", got)
	}
	list, err = ix.Search(context.Background(), domain.ContextQuery{Text: "cookie api"}, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := indexIDs(list); len(got) != 1 || got[0] != "s2" {
		t.Fatalf("AND search = %v, want s2", got)
	}
}

func TestIndexSearchEscapesSyntax(t *testing.T) {
	ix := newTestIndex(t)
	putIndexEntry(t, ix, domain.ContextEntry{ID: "escape", Title: "Syntax", Body: "session cookie near or"})
	raw := `"session" -cookie: NEAR( OR *`
	got, err := ix.Search(context.Background(), domain.ContextQuery{Text: raw}, true, time.Now())
	if err != nil {
		t.Fatalf("raw syntax: %v", err)
	}
	plain, err := ix.Search(context.Background(), domain.ContextQuery{Text: "session cookie near or"}, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	gotIDs, plainIDs := indexIDs(got), indexIDs(plain)
	sort.Strings(gotIDs)
	sort.Strings(plainIDs)
	if len(gotIDs) != len(plainIDs) || (len(gotIDs) == 1 && gotIDs[0] != plainIDs[0]) {
		t.Fatalf("raw = %v, plain = %v", gotIDs, plainIDs)
	}
}

func TestIndexSearchLiveFilterAndRank(t *testing.T) {
	ix := newTestIndex(t)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	putIndexEntry(t, ix, domain.ContextEntry{ID: "A", Title: "A", Body: "needle", CreatedAt: now})
	putIndexEntry(t, ix, domain.ContextEntry{ID: "B", Title: "B", Body: "needle", Supersedes: "A", CreatedAt: now})
	putIndexEntry(t, ix, domain.ContextEntry{ID: "C", Title: "C", Body: "needle", ValidUntil: now.Add(-time.Hour), CreatedAt: now})
	putIndexEntry(t, ix, domain.ContextEntry{ID: "D", Title: "D", Body: "needle", ValidUntil: now.Add(time.Hour), CreatedAt: now.Add(-time.Hour)})
	list, err := ix.Search(context.Background(), domain.ContextQuery{Text: "needle"}, true, now)
	if err != nil {
		t.Fatal(err)
	}
	got := indexIDs(list)
	if len(got) != 2 || got[0] != "B" || got[1] != "D" {
		t.Fatalf("live search = %v, want B,D", got)
	}
}

func TestIndexUpsertReplaces(t *testing.T) {
	ix := newTestIndex(t)
	putIndexEntry(t, ix, domain.ContextEntry{ID: "x", Title: "Old title", Body: "old body"})
	putIndexEntry(t, ix, domain.ContextEntry{ID: "x", Title: "New title", Body: "new body"})
	old, err := ix.Search(context.Background(), domain.ContextQuery{Text: "old"}, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(old) != 0 {
		t.Fatalf("old title still matches: %v", indexIDs(old))
	}
	fresh, err := ix.Search(context.Background(), domain.ContextQuery{Text: "new"}, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := indexIDs(fresh); len(got) != 1 || got[0] != "x" {
		t.Fatalf("new title search = %v", got)
	}
}

func TestIndexHighWaterRoundTrip(t *testing.T) {
	ix := newTestIndex(t)
	at := time.Date(2026, 10, 27, 12, 34, 56, 789, time.UTC)
	if err := ix.SetHighWater(context.Background(), at); err != nil {
		t.Fatal(err)
	}
	got, err := ix.HighWater(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(at) {
		t.Fatalf("high water = %s, want %s", got, at)
	}
}

func TestIndexRemove(t *testing.T) {
	ix := newTestIndex(t)
	putIndexEntry(t, ix, domain.ContextEntry{ID: "gone", Title: "Gone", Body: "needle"})
	if err := ix.Remove(context.Background(), "gone"); err != nil {
		t.Fatal(err)
	}
	list, err := ix.Search(context.Background(), domain.ContextQuery{Text: "needle"}, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("removed note still searchable: %v", indexIDs(list))
	}
	paths, err := ix.Paths(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := paths["gone"]; ok {
		t.Fatalf("path still present: %v", paths)
	}
}
