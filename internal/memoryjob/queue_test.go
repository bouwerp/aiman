package memoryjob

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

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
