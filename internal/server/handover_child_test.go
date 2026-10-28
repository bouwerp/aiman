package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/handoverchild"
)

// childSessions knows the worker and holds CreateChild until release closes,
// then fails it, so a run stays starting for as long as a test needs.
type childSessions struct {
	worker  domain.Session
	release chan struct{}
}

func (c *childSessions) Get(_ context.Context, id string) (domain.Session, error) {
	if id == c.worker.ID {
		return c.worker, nil
	}
	return domain.Session{}, errors.New("not found")
}

func (c *childSessions) CreateChild(ctx context.Context, _ domain.SessionConfig) (domain.Session, error) {
	select {
	case <-c.release:
	case <-ctx.Done():
	}
	return domain.Session{}, errors.New("no child in tests")
}

func (c *childSessions) Forget(context.Context, string) error { return nil }

type deadTerminal struct{}

func (deadTerminal) Alive(context.Context, domain.Session) bool { return false }
func (deadTerminal) Stop(context.Context, domain.Session) error { return nil }

// withHandoverChild attaches a real manager whose child launch blocks until
// the returned release func runs.
func withHandoverChild(t *testing.T, srv *Server, worker domain.Session) (*handoverchild.Store, func()) {
	t.Helper()
	base := shortTempDir(t)
	store := handoverchild.NewStore(filepath.Join(base, "runs"))
	sessions := &childSessions{worker: worker, release: make(chan struct{})}
	d := srv.HandoverDeps(store, func(string) bool { return true })
	d.Sessions = sessions
	d.Terminal = deadTerminal{}
	d.Candidates = func(string) []string { return []string{"claude"} }
	d.BaseDir = base
	d.Poll = 10 * time.Millisecond
	m := handoverchild.NewManager(d)
	srv.SetHandovers(m)
	released := false
	release := func() {
		if !released {
			released = true
			close(sessions.release)
		}
	}
	t.Cleanup(func() {
		release()
		m.Close()
	})
	return store, release
}

func TestWriteHandoverChildOwnsMemoryJob(t *testing.T) {
	q := testMemoryQueue(t)
	srv := newHandoverTestServer(t, paneText("$ go test"), todosWith("t1", "Ship it", "pending"), "")
	srv.SetMemoryJobs(q)
	sess := domain.Session{ID: "s1", Name: "WTB-12", WorkingDirectory: t.TempDir(), AgentName: "claude"}
	_, release := withHandoverChild(t, srv, sess)

	res, err := srv.writeHandover(context.Background(), sess, "archive")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Written || res.Child == "" {
		t.Fatalf("result %+v, want a written file and a child run", res)
	}
	if _, err := srv.ctxStore.Get(context.Background(), "handover-s1"); err != nil {
		t.Fatalf("mechanical note not stored at once: %v", err)
	}
	jobs, err := q.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("%d memory jobs enqueued while the child starts, want 0", len(jobs))
	}

	// A second trigger while the run is active joins it.
	again, err := srv.writeHandover(context.Background(), sess, "restart")
	if err != nil {
		t.Fatal(err)
	}
	if again.Child != res.Child {
		t.Fatalf("second trigger child %q, want active run %q", again.Child, res.Child)
	}

	release()
	eventually(t, 3*time.Second, func() bool {
		jobs, err := q.Pending(context.Background())
		return err == nil && len(jobs) == 1
	})
}

func TestPreCompactPointerHeldUntilRunEnds(t *testing.T) {
	srv := newHandoverTestServer(t, paneText("$ go test"), nil, "")
	st := newSteerer(&fakeSteerStore{}, &fakeSteerTarget{}, nil, time.Millisecond)
	srv.steer = st
	sess := domain.Session{ID: "s1", Name: "s1", WorkingDirectory: t.TempDir(), AgentName: "claude"}
	_, release := withHandoverChild(t, srv, sess)

	srv.notePreCompactHandover(context.Background(), sess)
	if !srv.handovers.Active("s1") {
		t.Fatal("no active child after pre_compact")
	}
	time.Sleep(20 * time.Millisecond)
	if n := st.Status("s1").Queued; n != 0 {
		t.Fatalf("%d notes queued while the child runs, want the pointer held", n)
	}

	release()
	waitQueued(t, st, "s1", 1)
	if got := st.Status("s1").Pending[0].Text; got != HandoverCompactionNote {
		t.Fatalf("released %q, want the compaction pointer", got)
	}
}

func TestWriteHandoverHiddenSessionWritesNothing(t *testing.T) {
	q := testMemoryQueue(t)
	srv := newHandoverTestServer(t, paneText("$ go test"), nil, "")
	srv.SetMemoryJobs(q)
	dir := t.TempDir()
	for _, role := range []domain.SessionRole{domain.SessionRoleReviewer, domain.SessionRoleHandover} {
		sess := domain.Session{ID: "s1", WorkingDirectory: dir, Role: role}
		res, err := srv.writeHandover(context.Background(), sess, "archive")
		if err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		if res.Written || res.Path != "" {
			t.Fatalf("%s: result %+v, want nothing written", role, res)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, domain.AimanHandoverFileName)); !os.IsNotExist(err) {
		t.Fatalf("handover file for a hidden session: %v", err)
	}
	if _, err := srv.ctxStore.Get(context.Background(), "handover-s1"); err == nil {
		t.Fatal("context note stored for a hidden session")
	}
	if jobs, _ := q.Pending(context.Background()); len(jobs) != 0 {
		t.Fatalf("%d memory jobs for a hidden session", len(jobs))
	}
}

func TestWriteHandoverKeepsChildFileForSameState(t *testing.T) {
	srv := newHandoverTestServer(t, paneText("$ go test"), nil, "")
	dir := t.TempDir()
	sess := domain.Session{ID: "s1", Name: "s1", WorkingDirectory: dir, AgentName: "claude"}
	path := filepath.Join(dir, domain.AimanHandoverFileName)
	if _, err := srv.writeHandover(context.Background(), sess, "archive"); err != nil {
		t.Fatal(err)
	}
	mech, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(mech)

	store, _ := withHandoverChild(t, srv, sess)
	if err := store.Put(handoverchild.Run{
		ID: "11111111-1111-1111-1111-111111111111", SessionID: "s1", Reason: "archive",
		MechanicalSHA: hex.EncodeToString(sum[:]), Applied: true, CleanedUp: true,
		Status: handoverchild.StatusComplete, StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	child := []byte("child rewrite\n")
	if err := os.WriteFile(path, child, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := srv.writeHandover(context.Background(), sess, "archive")
	if err != nil {
		t.Fatal(err)
	}
	if res.Written || res.Child != "" {
		t.Fatalf("result %+v, want the child file kept and no run", res)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, child) {
		t.Fatalf("file %q, want the child's rewrite kept", got)
	}
	if srv.handovers.Active("s1") {
		t.Fatal("a child started for an unchanged state")
	}
	runs, err := store.List()
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs %d (%v), want only the earlier one", len(runs), err)
	}
}
