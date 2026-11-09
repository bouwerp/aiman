//go:build acceptance

package server

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/ptyruntime/ptyfake"
)

// Serve load guard P1 (docs/specs/serve-load-guard.md, Behaviour 2): the
// stored session list is cached, invalidated by inserts, deletes and identity
// changes only, re-read after sessionListTTL, never written by a read, and the
// steer gate reads its row by id.

// cacheRepo counts List and Save calls on the repository behind serve. It
// embeds the sqlite repository so OnChange, ClearAgentEnded and the other
// optional methods serve finds by type assertion stay visible.
type cacheRepo struct {
	*sqlite.Repository
	lists atomic.Int64
	saves atomic.Int64
}

func (c *cacheRepo) List(ctx context.Context) ([]domain.Session, error) {
	c.lists.Add(1)
	return c.Repository.List(ctx)
}

func (c *cacheRepo) Save(ctx context.Context, s *domain.Session) error {
	c.saves.Add(1)
	return c.Repository.Save(ctx, s)
}

// cacheServer stores rows straight into a fresh database, then starts serve
// over a counting wrapper of it. It returns the database path so a test can
// open it again as a second process.
func cacheServer(t *testing.T, rows ...domain.Session) (*Server, *cacheRepo, *ptyfake.FakePTY, string) {
	t.Helper()
	path := filepath.Join(shortTempDir(t), "aiman.db")
	inner, err := sqlite.NewRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inner.Close() })
	for i := range rows {
		if rows[i].Status == "" {
			rows[i].Status = domain.SessionStatusActive
		}
		if rows[i].CreatedAt.IsZero() {
			rows[i].CreatedAt = time.Now()
		}
		if err := inner.Save(context.Background(), &rows[i]); err != nil {
			t.Fatal(err)
		}
	}
	repo := &cacheRepo{Repository: inner}
	pty := ptyfake.New()
	return New(nil, repo, &fakeRemote{}, nil, nil, pty, "test"), repo, pty, path
}

func setSessionListTTL(t *testing.T, d time.Duration) {
	t.Helper()
	old := sessionListTTL
	sessionListTTL = d
	t.Cleanup(func() { sessionListTTL = old })
}

func cacheRow(id string) domain.Session {
	return domain.Session{ID: id, Name: id, Group: "g", TerminalName: id, Backend: domain.BackendPTY,
		AgentName: "claude", WorkingDirectory: "/w/" + id}
}

func mustLoad(t *testing.T, srv *Server) []domain.Session {
	t.Helper()
	list, err := srv.loadSessions(context.Background())
	if err != nil {
		t.Fatalf("loadSessions: %v", err)
	}
	return list
}

func listed(list []domain.Session, id string) bool {
	for _, s := range list {
		if s.ID == id {
			return true
		}
	}
	return false
}

func TestSessionListTTLDefaultsToTwoSeconds(t *testing.T) {
	if sessionListTTL != 2*time.Second {
		t.Fatalf("sessionListTTL = %v, want 2s", sessionListTTL)
	}
}

func TestTwoSessionListsReadTheTableOnce(t *testing.T) {
	srv, repo, _, _ := cacheServer(t, cacheRow("c-1"))
	var out struct {
		Sessions []SessionInfo `json:"sessions"`
	}
	rpcOK(t, srv, "", "session.list", map[string]any{}, &out)
	rpcOK(t, srv, "", "session.list", map[string]any{}, &out)
	if n := repo.lists.Load(); n > 1 {
		t.Fatalf("two session.list calls within a second ran List %d times, want at most 1", n)
	}
	if len(out.Sessions) != 1 || out.Sessions[0].ID != "c-1" {
		t.Fatalf("sessions = %+v, want c-1", out.Sessions)
	}
}

func TestHookReportDoesNotInvalidateTheList(t *testing.T) {
	setSessionListTTL(t, time.Minute)
	row := cacheRow("c-1")
	row.HookState = domain.AgentStateIdle
	row.HookStateSource = "lifecycle"
	row.HookStateAt = time.Now().UTC()
	srv, repo, _, _ := cacheServer(t, row)
	mustLoad(t, srv)
	base := repo.lists.Load()

	resp := rpc(t, srv, "", "session.report_agent_session", map[string]any{
		"id": "c-1", "state": "working", "source": "lifecycle", "title": "busy",
	})
	if resp.Error != nil {
		t.Fatalf("report: %+v", resp.Error)
	}
	if repo.saves.Load() == 0 {
		t.Fatal("the hook report was not saved")
	}
	mustLoad(t, srv)
	if n := repo.lists.Load() - base; n != 0 {
		t.Fatalf("loadSessions after a hook-only report ran List %d times, want 0 (cache hit)", n)
	}
}

func TestNewRowInvalidatesTheList(t *testing.T) {
	setSessionListTTL(t, time.Minute)
	srv, repo, _, _ := cacheServer(t, cacheRow("c-1"))
	mustLoad(t, srv)
	base := repo.lists.Load()

	added := cacheRow("c-new")
	added.Status = domain.SessionStatusActive
	added.CreatedAt = time.Now()
	if err := repo.Save(context.Background(), &added); err != nil {
		t.Fatal(err)
	}
	list := mustLoad(t, srv)
	if !listed(list, "c-new") {
		t.Fatalf("a new row is missing from the next loadSessions: %+v", list)
	}
	if n := repo.lists.Load() - base; n != 1 {
		t.Fatalf("loadSessions after an insert ran List %d times, want 1", n)
	}
}

func TestDeletedRowLeavesTheList(t *testing.T) {
	setSessionListTTL(t, time.Minute)
	srv, repo, _, _ := cacheServer(t, cacheRow("c-1"), cacheRow("c-2"))
	if list := mustLoad(t, srv); !listed(list, "c-2") {
		t.Fatalf("c-2 missing before delete: %+v", list)
	}
	if err := repo.Delete(context.Background(), "c-2"); err != nil {
		t.Fatal(err)
	}
	list := mustLoad(t, srv)
	if listed(list, "c-2") {
		t.Fatalf("a deleted row is still in the next loadSessions: %+v", list)
	}
	if !listed(list, "c-1") {
		t.Fatalf("c-1 lost after deleting c-2: %+v", list)
	}
}

// A write by another process (aiman-trigger) fires no OnChange here; the TTL
// is what brings it in.
func TestListIsReReadAfterTheTTL(t *testing.T) {
	setSessionListTTL(t, 100*time.Millisecond)
	srv, repo, _, path := cacheServer(t, cacheRow("c-1"))
	mustLoad(t, srv)
	base := repo.lists.Load()

	other, err := sqlite.NewRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ext := cacheRow("c-ext")
	ext.Status = domain.SessionStatusActive
	ext.CreatedAt = time.Now()
	if err := other.Save(context.Background(), &ext); err != nil {
		t.Fatal(err)
	}
	mustLoad(t, srv)
	if n := repo.lists.Load() - base; n != 0 {
		t.Fatalf("loadSessions within the TTL ran List %d times, want 0", n)
	}

	time.Sleep(200 * time.Millisecond)
	list := mustLoad(t, srv)
	if n := repo.lists.Load() - base; n != 1 {
		t.Fatalf("loadSessions after the TTL ran List %d times, want 1", n)
	}
	if !listed(list, "c-ext") {
		t.Fatalf("a row written by another process is missing after the TTL: %+v", list)
	}
}

// The delivery gate must see a hook report at once: a cached idle row must
// never let a note into a permission menu.
func TestSteerGateReadsTheRowByID(t *testing.T) {
	setSessionListTTL(t, time.Minute)
	row := cacheRow("c-1")
	row.HookState = domain.AgentStateIdle
	row.HookStateSource = "lifecycle"
	row.HookStateAt = time.Now().UTC()
	srv, repo, pty, _ := cacheServer(t, row)
	pty.Add(ptyruntime.SessionInfo{ID: "c-1", Name: "c-1", Dir: "/w/c-1", Status: ptyruntime.StatusRunning})
	ctx := context.Background()
	mustLoad(t, srv) // the cache now holds the idle row
	base := repo.lists.Load()

	if ok, reason := srv.steerReady(ctx, "c-1", time.Time{}); !ok {
		t.Fatalf("steerReady on a fresh idle report = held %q, want ready", reason)
	}
	resp := rpc(t, srv, "", "session.report_agent_session", map[string]any{
		"id": "c-1", "state": "waiting_input", "source": "lifecycle", "message": "Allow edit to main.go?",
	})
	if resp.Error != nil {
		t.Fatalf("report: %+v", resp.Error)
	}
	ok, reason := srv.steerReady(ctx, "c-1", time.Time{})
	if ok || reason != steerHeldDialog {
		t.Fatalf("steerReady after a waiting_input report = %v %q, want held %q at once", ok, reason, steerHeldDialog)
	}
	if n := repo.lists.Load() - base; n != 0 {
		t.Fatalf("the steer gate ran List %d times, want 0 (it reads the row by id)", n)
	}
}

func TestSessionListNeverWrites(t *testing.T) {
	nameless := domain.Session{ID: "c-anon", TerminalName: "anon-term", Backend: domain.BackendPTY,
		WorkingDirectory: "/w/anon"}
	srv, repo, _, _ := cacheServer(t, nameless)
	var out struct {
		Sessions []SessionInfo `json:"sessions"`
	}
	rpcOK(t, srv, "", "session.list", map[string]any{}, &out)
	if n := repo.saves.Load(); n != 0 {
		t.Fatalf("session.list saved %d rows, want 0 (the name backfill is not on the read path)", n)
	}
}
