package memoryjob

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	_ "modernc.org/sqlite"
)

func openQueue(t *testing.T) *Queue {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	q, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestEnqueueReplacesPendingForSession(t *testing.T) {
	q := openQueue(t)
	ctx := context.Background()
	if err := q.Enqueue(ctx, Job{ID: "j1", SessionID: "s1", Source: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx, Job{ID: "j2", SessionID: "s1", Source: "second"}); err != nil {
		t.Fatal(err)
	}
	jobs, err := q.Pending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != "j2" || jobs[0].Source != "second" {
		t.Fatalf("pending = %+v", jobs)
	}
}

func TestDoneRemovesFromPending(t *testing.T) {
	q := openQueue(t)
	ctx := context.Background()
	if err := q.Enqueue(ctx, Job{ID: "j1", SessionID: "s1", Created: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := q.Done(ctx, "j1"); err != nil {
		t.Fatal(err)
	}
	jobs, err := q.Pending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("pending = %+v", jobs)
	}
	if err := q.Done(ctx, "j1"); err != nil {
		t.Fatalf("second done: %v", err)
	}
}

func TestEnqueueRoundTripsCandidates(t *testing.T) {
	q := openQueue(t)
	ctx := context.Background()
	want := []domain.MemoryCandidate{
		{Type: domain.ContextTypeDecision, Title: "Use COALESCE", Abstract: "keep fields", Body: "Discovery writes blanks."},
		{Title: "Second", Body: "b"},
	}
	if err := q.Enqueue(ctx, Job{ID: "j1", SessionID: "s1", Candidates: want}); err != nil {
		t.Fatal(err)
	}
	jobs, err := q.Pending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || len(jobs[0].Candidates) != 2 {
		t.Fatalf("pending = %+v", jobs)
	}
	for i := range want {
		if jobs[0].Candidates[i] != want[i] {
			t.Fatalf("candidate %d = %+v, want %+v", i, jobs[0].Candidates[i], want[i])
		}
	}
}

func TestOpenMigratesTableWithoutCandidates(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE memory_jobs(
		id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL,
		reason TEXT,
		group_name TEXT,
		repo_name TEXT,
		label TEXT,
		source TEXT,
		created INTEGER NOT NULL,
		done INTEGER NOT NULL DEFAULT 0
	);`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memory_jobs(id, session_id, reason, group_name, repo_name, label, source, created, done) VALUES('old', 's0', '', '', '', '', 'legacy', 1, 0)`); err != nil {
		t.Fatal(err)
	}
	q, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(db); err != nil {
		t.Fatalf("second open: %v", err)
	}
	ctx := context.Background()
	if err := q.Enqueue(ctx, Job{ID: "new", SessionID: "s1", Candidates: []domain.MemoryCandidate{{Title: "t"}}}); err != nil {
		t.Fatal(err)
	}
	jobs, err := q.Pending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || jobs[0].ID != "old" || jobs[0].Source != "legacy" || jobs[0].Candidates != nil {
		t.Fatalf("pending = %+v", jobs)
	}
	if len(jobs[1].Candidates) != 1 || jobs[1].Candidates[0].Title != "t" {
		t.Fatalf("new job = %+v", jobs[1])
	}
}

func TestJobOmitsEmptyCandidatesFromJSON(t *testing.T) {
	q := openQueue(t)
	ctx := context.Background()
	if err := q.Enqueue(ctx, Job{ID: "j1", SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	jobs, err := q.Pending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Candidates != nil {
		t.Fatalf("pending = %+v", jobs)
	}
	b, err := json.Marshal(jobs[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "candidates") {
		t.Fatalf("json = %s", b)
	}
}
