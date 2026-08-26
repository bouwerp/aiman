package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestDSNCarriesBusyTimeout(t *testing.T) {
	got := dsn("/home/code/.aiman/aiman.db")
	if !strings.Contains(got, "_pragma=busy_timeout(5000)") {
		t.Fatalf("dsn missing busy_timeout: %q", got)
	}
	if !strings.HasPrefix(got, "/home/code/.aiman/aiman.db?") {
		t.Fatalf("dsn should keep the path intact: %q", got)
	}
	// A '?' in the path would be read as the query separator, so it is left be.
	odd := "/tmp/what?/aiman.db"
	if dsn(odd) != odd {
		t.Fatalf("a path containing '?' must be passed through: %q", dsn(odd))
	}
}

// Several processes share this file — the dashboard, serve, the trigger daemon,
// one-shot CLI calls — and SQLite's default is to fail immediately with
// "database is locked" the moment two of them overlap. That surfaced as a PTY
// holder failing to start at all, which is a session failing to start.
func TestConcurrentWritersDoNotReportDatabaseLocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aiman.db")

	const writers = 6
	repos := make([]*Repository, 0, writers)
	for i := 0; i < writers; i++ {
		// Each stands in for a separate process opening the same file.
		r, err := NewRepository(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		t.Cleanup(func() { _ = r.Close() })
		repos = append(repos, r)
	}

	var wg sync.WaitGroup
	errs := make(chan error, writers*10)
	for i, r := range repos {
		wg.Add(1)
		go func(i int, r *Repository) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				s := &domain.Session{
					ID:        string(rune('a'+i)) + "-" + string(rune('0'+j)),
					Branch:    "b",
					Status:    domain.SessionStatusActive,
					CreatedAt: time.Now(),
					UpdatedAt: time.Now(),
				}
				if err := r.Save(context.Background(), s); err != nil {
					errs <- err
					return
				}
			}
		}(i, r)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if strings.Contains(err.Error(), "locked") || strings.Contains(err.Error(), "SQLITE_BUSY") {
			t.Fatalf("concurrent writers must wait, not fail: %v", err)
		}
		t.Fatalf("unexpected error: %v", err)
	}
}
