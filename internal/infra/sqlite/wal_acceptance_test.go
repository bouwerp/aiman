//go:build acceptance

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// Serve load guard P1 (docs/specs/serve-load-guard.md, Behaviour 1): the file
// runs in WAL with synchronous NORMAL, one writer connection and a bounded
// reader pool, a second process can write while this one reads, Close leaves
// no WAL behind, and OnChange tells identity changes from hook-only writes.

func openWAL(t *testing.T, path string) *Repository {
	t.Helper()
	r, err := NewRepository(path)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func pragmaString(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var v string
	if err := db.QueryRowContext(context.Background(), "PRAGMA "+name).Scan(&v); err != nil {
		t.Fatalf("PRAGMA %s: %v", name, err)
	}
	return v
}

func TestWALModeAndSynchronousNormalOnBothPools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aiman.db")
	// A file created by an older binary is in the rollback journal mode.
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec("CREATE TABLE legacy (a TEXT)"); err != nil {
		t.Fatal(err)
	}
	_ = old.Close()

	r := openWAL(t, path)
	if got := pragmaString(t, r.Reader(), "journal_mode"); got != "wal" {
		t.Fatalf("journal_mode = %q, want wal", got)
	}
	if got := pragmaString(t, r.Reader(), "synchronous"); got != "1" {
		t.Fatalf("reader synchronous = %q, want 1 (NORMAL)", got)
	}
	if got := pragmaString(t, r.DB(), "synchronous"); got != "1" {
		t.Fatalf("writer synchronous = %q, want 1 (NORMAL)", got)
	}
}

func TestWriterAndReaderPools(t *testing.T) {
	r := openWAL(t, filepath.Join(t.TempDir(), "aiman.db"))
	if r.DB() == nil || r.Reader() == nil {
		t.Fatal("DB() and Reader() must both be open")
	}
	if r.DB() == r.Reader() {
		t.Fatal("DB() and Reader() must be distinct pools")
	}
	if got := r.DB().Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("writer MaxOpenConnections = %d, want 1", got)
	}
	if got := r.Reader().Stats().MaxOpenConnections; got <= 1 {
		t.Fatalf("reader MaxOpenConnections = %d, want more than 1", got)
	}
}

func walRow(id string) *domain.Session {
	return &domain.Session{
		ID: id, Name: id, Group: "g", Branch: "b", Backend: domain.BackendPTY,
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}
}

// aiman-trigger opens the same file as serve. Its write must not wait on a
// read serve holds open.
func TestSecondProcessWritesWhileReaderIsOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aiman.db")
	serve := openWAL(t, path)
	ctx := context.Background()
	for _, id := range []string{"r1", "r2", "r3"} {
		if err := serve.Save(ctx, walRow(id)); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := serve.Reader().QueryContext(ctx, "SELECT "+sessionSelectCols+" FROM sessions ORDER BY updated_at DESC;")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("reader returned no rows: %v", rows.Err())
	}

	trigger, err := NewRepository(path)
	if err != nil {
		t.Fatalf("second NewRepository: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- trigger.Save(ctx, walRow("from-trigger")) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second process Save while a reader is open: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second process Save blocked for 2s behind an open reader (database is locked)")
	}
	_ = rows.Close()

	// serve closing while the trigger still holds the file does not fail.
	if err := serve.Close(); err != nil {
		t.Fatalf("Close while another process holds the file: %v", err)
	}
	if err := trigger.Close(); err != nil {
		t.Fatalf("trigger Close: %v", err)
	}
}

func TestCloseLeavesNoWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aiman.db")
	r, err := NewRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		if err := r.Save(ctx, walRow(id)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.List(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path + "-wal"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s-wal remains after Close (stat err %v), want it checkpointed away", path, err)
	}
	// The file alone holds every row.
	again := openWAL(t, path)
	list, err := again.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("rows after reopen = %d, want 3", len(list))
	}
}

// changeLog records what OnChange reports.
type changeLog struct {
	mu    sync.Mutex
	kinds []ChangeKind
}

func (c *changeLog) add(k ChangeKind) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.kinds = append(c.kinds, k)
}

func (c *changeLog) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.kinds = nil
}

// wait returns what was reported once at least one kind arrived, or after
// half a second.
func (c *changeLog) wait() []ChangeKind {
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		c.mu.Lock()
		got := append([]ChangeKind(nil), c.kinds...)
		c.mu.Unlock()
		if len(got) > 0 || time.Now().After(deadline) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func hasKind(kinds []ChangeKind, k ChangeKind) bool {
	for _, got := range kinds {
		if got == k {
			return true
		}
	}
	return false
}

// changeBase is a stored row with every identity and hook field set, so each
// case below changes a value rather than filling an empty one.
func changeBase(id string) *domain.Session {
	return &domain.Session{
		ID: id, Name: "name", Group: "group", ParentID: "parent",
		Role: domain.SessionRoleReviewer, Status: domain.SessionStatusActive,
		Backend: domain.BackendPTY, Branch: "branch", WorkingDirectory: "/w/one",
		ReviewPR: "o/r#1", FixPR: "o/r#2", AgentName: "claude",
		HookState: domain.AgentStateIdle, HookStateSource: "lifecycle",
		HookStateAt: time.Now().Add(-time.Minute).UTC(), AgentTitle: "title",
		CreatedAt: time.Now(),
	}
}

// changeRepo stores the base row and returns the repository with a fresh log
// subscribed after that insert.
func changeRepo(t *testing.T) (*Repository, *changeLog) {
	t.Helper()
	r := openWAL(t, filepath.Join(t.TempDir(), "aiman.db"))
	log := &changeLog{}
	r.OnChange(log.add)
	if err := r.Save(context.Background(), changeBase("s1")); err != nil {
		t.Fatal(err)
	}
	return r, log
}

func TestOnChangeInsertAndDeleteAreIdentity(t *testing.T) {
	r, log := changeRepo(t)
	if got := log.wait(); !hasKind(got, ChangeIdentity) {
		t.Fatalf("insert of a new session reported %v, want ChangeIdentity", got)
	}
	log.reset()
	if err := r.Delete(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if got := log.wait(); !hasKind(got, ChangeIdentity) {
		t.Fatalf("Delete reported %v, want ChangeIdentity", got)
	}
}

func TestOnChangeKindOfSave(t *testing.T) {
	later := time.Now().UTC()
	cases := []struct {
		name   string
		mutate func(*domain.Session)
		want   ChangeKind
	}{
		{"name", func(s *domain.Session) { s.Name = "renamed" }, ChangeIdentity},
		{"group", func(s *domain.Session) { s.Group = "other-group" }, ChangeIdentity},
		{"parent", func(s *domain.Session) { s.ParentID = "other-parent" }, ChangeIdentity},
		{"role", func(s *domain.Session) { s.Role = domain.SessionRoleConsult }, ChangeIdentity},
		{"status", func(s *domain.Session) { s.Status = domain.SessionStatusInactive }, ChangeIdentity},
		{"backend", func(s *domain.Session) { s.Backend = domain.BackendLegacyTmux }, ChangeIdentity},
		{"branch", func(s *domain.Session) { s.Branch = "other-branch" }, ChangeIdentity},
		{"working directory", func(s *domain.Session) { s.WorkingDirectory = "/w/two" }, ChangeIdentity},
		{"review pr", func(s *domain.Session) { s.ReviewPR = "o/r#3" }, ChangeIdentity},
		{"fix pr", func(s *domain.Session) { s.FixPR = "o/r#4" }, ChangeIdentity},
		{"hook state", func(s *domain.Session) { s.HookState = domain.AgentStateWorking }, ChangeOther},
		{"hook state at", func(s *domain.Session) { s.HookStateAt = later }, ChangeOther},
		{"agent title", func(s *domain.Session) { s.AgentTitle = "new title" }, ChangeOther},
		{"agent ended", func(s *domain.Session) { s.AgentEnded = true }, ChangeOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, log := changeRepo(t)
			ctx := context.Background()
			stored, err := r.Get(ctx, "s1")
			if err != nil {
				t.Fatal(err)
			}
			log.wait() // the base insert
			log.reset()
			tc.mutate(stored)
			if err := r.Save(ctx, stored); err != nil {
				t.Fatal(err)
			}
			got := log.wait()
			if !hasKind(got, tc.want) {
				t.Fatalf("Save changing %s reported %v, want %v", tc.name, got, tc.want)
			}
			if tc.want == ChangeOther && hasKind(got, ChangeIdentity) {
				t.Fatalf("Save changing only %s reported ChangeIdentity (%v); hook writes must not invalidate", tc.name, got)
			}
		})
	}
}
