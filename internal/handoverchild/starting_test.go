package handoverchild

import (
	"path/filepath"
	"testing"
	"time"
)

func TestManagerStarting(t *testing.T) {
	if got := (*Manager)(nil).Starting(); got != 0 {
		t.Fatalf("nil Starting = %d, want 0", got)
	}
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	store := NewStore(filepath.Join(t.TempDir(), "runs"))
	m := NewManager(Deps{Store: store, Now: func() time.Time { return now }, Timeout: 10 * time.Minute})
	t.Cleanup(m.Close)
	if got := m.Starting(); got != 0 {
		t.Fatalf("empty store Starting = %d, want 0", got)
	}
	runs := []Run{
		{ID: "a", Status: StatusStarting, StartedAt: now.Add(-time.Minute)},
		{ID: "b", Status: StatusStarting, StartedAt: now.Add(-9 * time.Minute)},
		{ID: "c", Status: StatusStarting, StartedAt: now.Add(-11 * time.Minute)},
		{ID: "d", Status: StatusRunning, StartedAt: now.Add(-time.Minute)},
		{ID: "e", Status: StatusComplete, StartedAt: now.Add(-time.Minute)},
	}
	for _, r := range runs {
		if err := store.Put(r); err != nil {
			t.Fatal(err)
		}
	}
	if got := m.Starting(); got != 2 {
		t.Fatalf("Starting = %d, want 2 (young starting runs only)", got)
	}
}
