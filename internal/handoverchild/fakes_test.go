package handoverchild

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/handover"
	"github.com/bouwerp/aiman/internal/memoryjob"
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
		ID: fmt.Sprintf("child-%d", len(f.created)), Name: cfg.Name, ParentID: cfg.ParentID, Role: cfg.Role,
		WorktreePath: cfg.PreparedWorktree, WorkingDirectory: cfg.PreparedWorktree, Status: domain.SessionStatusActive,
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

func (f *fakeSessions) put(s domain.Session) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[s.ID] = s
}

func (f *fakeSessions) remove(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, id)
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

// fakeTerminal reports every session alive until it is stopped or killed.
type fakeTerminal struct {
	mu      sync.Mutex
	dead    map[string]bool
	stopped []string
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
	f.dead[s.ID] = true
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

type steered struct{ session, text string }

type fakeSteer struct {
	mu    sync.Mutex
	notes []steered
}

func (f *fakeSteer) SteerAsAiman(sessionID, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, steered{sessionID, text})
}

func (f *fakeSteer) all() []steered {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]steered(nil), f.notes...)
}

type ledgerEvent struct {
	session string
	kind    domain.EventKind
	payload map[string]any
}

type fakeLedger struct {
	mu     sync.Mutex
	events []ledgerEvent
}

func (f *fakeLedger) Append(_ context.Context, sessionID string, kind domain.EventKind, payload map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ledgerEvent{sessionID, kind, payload})
}

func (f *fakeLedger) all() []ledgerEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ledgerEvent(nil), f.events...)
}

type fakeContext struct {
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

type fakeJobs struct {
	mu   sync.Mutex
	jobs []memoryjob.Job
}

func (f *fakeJobs) Enqueue(_ context.Context, j memoryjob.Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs = append(f.jobs, j)
	return nil
}

func (f *fakeJobs) all() []memoryjob.Job {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]memoryjob.Job(nil), f.jobs...)
}

// fakeClock only moves when a test advances it, so timeouts are deterministic.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

const mechanicalBody = "# Handover: w1\n\nmechanical body\n"

// rig is a manager over fakes with one worker session, w1, whose working
// directory already holds the mechanical handover.
type rig struct {
	m        *Manager
	store    *Store
	sessions *fakeSessions
	term     *fakeTerminal
	steer    *fakeSteer
	ledger   *fakeLedger
	ctx      *fakeContext
	jobs     *fakeJobs
	clock    *fakeClock
	worker   domain.Session
	base     string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	root := t.TempDir()
	workDir := filepath.Join(root, "repo@w1")
	if err := os.MkdirAll(workDir, 0o750); err != nil {
		t.Fatal(err)
	}
	worker := domain.Session{
		ID: "w1", Name: "w1", Group: "g", RepoName: "o/r", AgentName: "claude",
		WorktreePath: workDir, WorkingDirectory: workDir, AgentSessionPath: "/tmp/transcript.jsonl",
		RemoteHost: "h", Backend: domain.BackendPTY,
	}
	if err := os.WriteFile(filepath.Join(workDir, domain.AimanHandoverFileName), mechanicalRender(), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &rig{
		store:    NewStore(filepath.Join(root, "runs")),
		sessions: &fakeSessions{sessions: map[string]domain.Session{"w1": worker}},
		term:     &fakeTerminal{dead: map[string]bool{}},
		steer:    &fakeSteer{},
		ledger:   &fakeLedger{},
		ctx:      &fakeContext{},
		jobs:     &fakeJobs{},
		clock:    &fakeClock{t: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)},
		worker:   worker,
		base:     filepath.Join(root, "handovers"),
	}
	r.m = r.newManager()
	t.Cleanup(r.m.Close)
	return r
}

func (r *rig) newManager() *Manager {
	return NewManager(Deps{
		Store:      r.store,
		Sessions:   r.sessions,
		Terminal:   r.term,
		Steer:      r.steer,
		Ledger:     r.ledger,
		Context:    r.ctx,
		Jobs:       r.jobs,
		Installed:  func(a string) bool { return a == "claude" },
		Candidates: func(worker string) []string { return []string{"codex", worker} },
		BaseDir:    r.base,
		Now:        r.clock.Now,
		Poll:       2 * time.Millisecond,
		Timeout:    10 * time.Minute,
	})
}

func mechanicalRender() []byte {
	return []byte(domain.AimanScaffoldHeader + mechanicalBody)
}

func (r *rig) start(t *testing.T, reason string) string {
	t.Helper()
	id, started, err := r.m.Start(context.Background(), StartRequest{
		Worker: r.worker, Reason: reason, Mechanical: mechanicalRender(), MechanicalSHA: "sha-1",
		Label: "w1", Abstract: "mechanical abstract",
	})
	if err != nil || !started {
		t.Fatalf("start: id %q started %v err %v", id, started, err)
	}
	return id
}

// waitRun polls the store until pred holds.
func (r *rig) waitRun(t *testing.T, id string, pred func(Run) bool) Run {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		run, err := r.store.Get(id)
		if err == nil && pred(run) {
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s: condition not met; last %+v err %v", id, run, err)
		}
		time.Sleep(time.Millisecond)
	}
}

func (r *rig) waitRunning(t *testing.T, id string) Run {
	return r.waitRun(t, id, func(x Run) bool { return x.Status == StatusRunning })
}

func (r *rig) waitCleaned(t *testing.T, id string) Run {
	return r.waitRun(t, id, func(x Run) bool { return x.CleanedUp })
}

func validChildHandover() string {
	var b strings.Builder
	b.WriteString("# Handover: w1\n\n")
	for _, h := range handover.RequiredSections {
		b.WriteString(h + "\n\nchild text for " + h + "\n\n")
	}
	return b.String()
}

const childMemories = `{"candidates":[{"type":"decision","title":"Use COALESCE","abstract":"Save keeps fields","body":"Discovery would blank them."}]}`

// writeOutputs writes the child's outputs and, last, the done marker.
func writeOutputs(t *testing.T, dir, md, memories string) {
	t.Helper()
	if md != "" {
		if err := os.WriteFile(filepath.Join(dir, handover.HandoverFile), []byte(md), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if memories != "" {
		if err := os.WriteFile(filepath.Join(dir, handover.MemoriesFile), []byte(memories), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, handover.DoneFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (r *rig) workerFile(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.worker.WorkingDirectory, domain.AimanHandoverFileName))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
