package verify

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStorePutGetList(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := Review{ID: "a", WorkerID: "w", Agent: "codex", Status: StatusRunning, StartedAt: t0}
	b := Review{ID: "b", WorkerID: "w", Agent: "claude", Status: StatusComplete, Required: 1, StartedAt: t0.Add(time.Minute)}
	if err := s.Put(a); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(b); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("a")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "a" || got.Agent != "codex" || got.Status != StatusRunning || !got.StartedAt.Equal(t0) {
		t.Fatalf("got %+v", got)
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "b" || list[1].ID != "a" {
		t.Fatalf("list = %+v", list)
	}
}

func TestStoreGetMissing(t *testing.T) {
	if _, err := NewStore(t.TempDir()).Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestStorePutIsAtomic(t *testing.T) {
	dir := t.TempDir()
	if err := NewStore(dir).Put(Review{ID: "a", Status: StatusStarting}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("left temp file %s", e.Name())
		}
	}
	if len(entries) != 1 || entries[0].Name() != "a.json" {
		t.Fatalf("entries = %v", entries)
	}
	info, err := os.Stat(filepath.Join(dir, "a.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestReviewPassed(t *testing.T) {
	cases := []struct {
		r    Review
		want bool
	}{
		{Review{Status: StatusComplete, Evidence: Evidence{Command: "make test"}}, true},
		{Review{Status: StatusComplete}, false},
		{Review{Status: StatusComplete, Required: 1, Evidence: Evidence{Command: "make test"}}, false},
		{Review{Status: StatusIncomplete}, false},
		{Review{Status: StatusTimedOut}, false},
	}
	for _, c := range cases {
		if got := c.r.Passed(); got != c.want {
			t.Fatalf("Passed(%+v) = %v", c.r, got)
		}
	}
}
