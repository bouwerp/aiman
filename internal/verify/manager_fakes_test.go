package verify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

type fakeSessions struct {
	mu        sync.Mutex
	sessions  map[string]domain.Session
	created   []domain.SessionConfig
	forgotten []string
	createErr error
}

func (f *fakeSessions) Get(_ context.Context, id string) (domain.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok {
		return domain.Session{}, errors.New("session not found")
	}
	return s, nil
}

func (f *fakeSessions) CreateChild(_ context.Context, cfg domain.SessionConfig) (domain.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, cfg)
	if f.createErr != nil {
		return domain.Session{}, f.createErr
	}
	s := domain.Session{
		ID: fmt.Sprintf("rev-%d", len(f.created)), Name: cfg.Name, Group: cfg.Group, ParentID: cfg.ParentID, Role: cfg.Role,
		WorktreePath: cfg.PreparedWorktree, WorkingDirectory: filepath.Join(cfg.PreparedWorktree, cfg.Directory),
		Status: domain.SessionStatusActive,
	}
	f.sessions[s.ID] = s
	return s, nil
}

func (f *fakeSessions) Forget(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgotten = append(f.forgotten, id)
	delete(f.sessions, id)
	return nil
}

func (f *fakeSessions) createdConfigs() []domain.SessionConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.SessionConfig(nil), f.created...)
}

func (f *fakeSessions) forgottenIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.forgotten...)
}

func (f *fakeSessions) setEnded(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.sessions[id]
	s.AgentEnded = true
	f.sessions[id] = s
}

// fakeTerminal reports every session alive until it is stopped or marked dead.
type fakeTerminal struct {
	mu      sync.Mutex
	dead    map[string]bool
	stopped []string
	// stopKills makes Stop end the process, as a real terminal kill does.
	stopKills bool
}

func (f *fakeTerminal) Alive(_ context.Context, s domain.Session) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.dead[s.ID]
}

func (f *fakeTerminal) Stop(_ context.Context, s domain.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, s.ID)
	if f.stopKills {
		f.dead[s.ID] = true
	}
	return nil
}

func (f *fakeTerminal) kill(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dead[id] = true
}

func (f *fakeTerminal) stoppedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.stopped...)
}

type fakeTodos struct {
	mu      sync.Mutex
	lists   map[string]*domain.TodoList
	next    int
	authors []domain.TodoAuthor
}

func (f *fakeTodos) Get(id string) (domain.TodoList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if l, ok := f.lists[id]; ok {
		cp := *l
		cp.Items = append([]domain.TodoItem(nil), l.Items...)
		return cp, nil
	}
	return domain.TodoList{SessionID: id}, nil
}

func (f *fakeTodos) Mutate(_ context.Context, id string, author domain.TodoAuthor, fn func(*domain.TodoList) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.lists[id]
	if !ok {
		l = &domain.TodoList{SessionID: id}
		f.lists[id] = l
	}
	work := *l
	work.Items = append([]domain.TodoItem(nil), l.Items...)
	if err := fn(&work); err != nil {
		return err
	}
	for i := range work.Items {
		if work.Items[i].ID == "" {
			f.next++
			work.Items[i].ID = fmt.Sprintf("t%d", f.next)
			work.Items[i].UpdatedBy = author
		}
	}
	*l = work
	f.authors = append(f.authors, author)
	return nil
}

// items is a copy of the session's list items.
func (f *fakeTodos) items(id string) []domain.TodoItem {
	l, _ := f.Get(id)
	return l.Items
}

func (f *fakeTodos) authorList() []domain.TodoAuthor {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.TodoAuthor(nil), f.authors...)
}

type fakeNotifier struct {
	mu       sync.Mutex
	finished []Review
}

func (f *fakeNotifier) ReviewFinished(_ context.Context, _ domain.Session, r Review) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finished = append(f.finished, r)
}

func (f *fakeNotifier) all() []Review {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Review(nil), f.finished...)
}

// fakeContext records Put; the rest of the store is unused by the manager.
type fakeContext struct {
	domain.ContextStore
	mu   sync.Mutex
	puts []domain.ContextEntry
}

func (f *fakeContext) Put(_ context.Context, e domain.ContextEntry) (domain.ContextEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, e)
	return e, nil
}

func (f *fakeContext) all() []domain.ContextEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.ContextEntry(nil), f.puts...)
}

type mrig struct {
	m        *Manager
	sessions *fakeSessions
	term     *fakeTerminal
	todos    *fakeTodos
	notify   *fakeNotifier
	ctx      *fakeContext
	store    *Store
	worker   domain.Session
}

// newMRig builds a manager over a real worker repo at <tmp>/repo@feat with
// its agent working in the sub directory.
func newMRig(t *testing.T, cfg Config) *mrig {
	t.Helper()
	gitAvailable(t)
	root := t.TempDir()
	wt := filepath.Join(root, "repo@feat")
	runTestGit(t, root, "init", "-b", "master", wt)
	runTestGit(t, wt, "config", "user.email", "test@example.com")
	runTestGit(t, wt, "config", "user.name", "Test")
	if err := os.MkdirAll(filepath.Join(wt, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "sub", "a.go"), []byte("package a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, wt, "add", ".")
	runTestGit(t, wt, "commit", "-m", "base")
	if err := os.WriteFile(filepath.Join(wt, "sub", "a.go"), []byte("package a\n\nvar x = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "sub", domain.AimanTaskFileName), []byte("Add x.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	worker := domain.Session{
		ID: "w1", Name: "impl", Group: "PROJ-1", AgentName: "Claude Code", BaseBranch: "master",
		WorktreePath: wt, WorkingDirectory: filepath.Join(wt, "sub"), RepoName: "o/repo", Status: domain.SessionStatusActive,
	}
	r := &mrig{
		sessions: &fakeSessions{sessions: map[string]domain.Session{worker.ID: worker}},
		term:     &fakeTerminal{dead: map[string]bool{}},
		todos:    &fakeTodos{lists: map[string]*domain.TodoList{}},
		notify:   &fakeNotifier{},
		ctx:      &fakeContext{},
		store:    NewStore(filepath.Join(root, "reviews")),
		worker:   worker,
	}
	if cfg.Agents == nil {
		cfg.Agents = []string{"claude", "codex"}
	}
	r.m = NewManager(cfg, Deps{
		Store: r.store, Sessions: r.sessions, Terminal: r.term, Todos: r.todos,
		Context: r.ctx, Notifier: r.notify,
		Installed: func(c string) bool { return c == "claude" || c == "codex" },
		Poll:      10 * time.Millisecond,
	})
	t.Cleanup(r.m.Close)
	return r
}

// eventually polls cond for up to 2s.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// waitStatus waits until the stored review reaches status.
func (r *mrig) waitStatus(t *testing.T, id string, status Status) Review {
	t.Helper()
	var got Review
	eventually(t, "status "+string(status), func() bool {
		rv, err := r.m.Status(id)
		got = rv
		return err == nil && rv.Status == status
	})
	return got
}
