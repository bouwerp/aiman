package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/memoryjob"
)

func testMemoryQueue(t *testing.T) *memoryjob.Queue {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(shortTempDir(t), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	q, err := memoryjob.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestHandoverEnqueuesMemoryJob(t *testing.T) {
	q := testMemoryQueue(t)
	srv := newHandoverTestServer(t, paneText("$ go test\nFAIL parser"), todosWith("t2", "Fix parser", "active"), "")
	srv.SetMemoryJobs(q)
	sess := domain.Session{ID: "s1", Name: "WTB-12", WorkingDirectory: t.TempDir()}
	if _, err := srv.writeHandover(context.Background(), sess, "restart"); err != nil {
		t.Fatal(err)
	}
	jobs, err := q.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("jobs = %+v", jobs)
	}
	if jobs[0].Reason != "restart" || jobs[0].Label != "WTB-12" || !strings.Contains(jobs[0].Source, "Fix parser") {
		t.Fatalf("job = %+v", jobs[0])
	}
}

func TestUnchangedHandoverDoesNotEnqueue(t *testing.T) {
	q := testMemoryQueue(t)
	srv := newHandoverTestServer(t, paneText("screen"), nil, "")
	srv.SetMemoryJobs(q)
	sess := domain.Session{ID: "s1", Name: "WTB-12", WorkingDirectory: t.TempDir()}
	if _, err := srv.writeHandover(context.Background(), sess, "restart"); err != nil {
		t.Fatal(err)
	}
	jobs, err := q.Pending(context.Background())
	if err != nil || len(jobs) != 1 {
		t.Fatalf("first = %+v %v", jobs, err)
	}
	if err := q.Done(context.Background(), jobs[0].ID); err != nil {
		t.Fatal(err)
	}
	res, err := srv.writeHandover(context.Background(), sess, "restart")
	if err != nil {
		t.Fatal(err)
	}
	if res.Written {
		t.Fatal("second write changed the file")
	}
	jobs, err = q.Pending(context.Background())
	if err != nil || len(jobs) != 0 {
		t.Fatalf("pending after unchanged write = %+v %v", jobs, err)
	}
}

func startMemoryServer(t *testing.T, q *memoryjob.Queue) string {
	t.Helper()
	dir := shortTempDir(t)
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv := New(ln, nil, nil, nil, nil, nil, "test")
	srv.SetMemoryJobs(q)
	go func() { _ = srv.Serve(ctx) }()
	t.Cleanup(cancel)
	return SocketPath(dir)
}

func TestMemoryJobsAndDoneOverSocket(t *testing.T) {
	q := testMemoryQueue(t)
	if err := q.Enqueue(context.Background(), memoryjob.Job{
		ID: "j1", SessionID: "s1", Reason: "restart", Label: "WTB-1", Created: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	sock := startMemoryServer(t, q)
	resp, err := Call(sock, "memory.jobs", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Type string          `json:"type"`
		Jobs []memoryjob.Job `json:"jobs"`
	}
	raw, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.Type != "memory_jobs" || len(res.Jobs) != 1 || res.Jobs[0].ID != "j1" {
		t.Fatalf("jobs = %s", raw)
	}
	done, err := Call(sock, "memory.done", map[string]any{"id": "j1"})
	if err != nil || done.Error != nil {
		t.Fatalf("done: %v %+v", err, done.Error)
	}
	jobs, err := q.Pending(context.Background())
	if err != nil || len(jobs) != 0 {
		t.Fatalf("pending = %+v %v", jobs, err)
	}
}
