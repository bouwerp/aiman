//go:build pileup_a

package ui

// Contract (docs/specs/client-pileup-guard.md, Behaviour P2, TUI side; the
// usecase.HostBudget API is pinned in
// internal/usecase/host_budget_acceptance_test.go):
//
//   - Model field `hostBudget *usecase.HostBudget`. NewModel sets it to
//     usecase.NewHostBudget(time.Now); tests replace it. The budget is keyed
//     by remote host (config.Remote.Host).
//     Tick and event fetches (resources.get poll, git status tick,
//     todo.summary, todo.get / ci.get / usage.get on an event) call
//     TryAcquire before anything else; with no slot they return a nil
//     command and set no in-flight flag. A skipped event fetch is remembered
//     per session and method and started when a slot frees, which the model
//     sees on the next result message it handles.
//     Selection fetches (preview, git status, todo.get, ci.get, usage.get with
//     its session.get) call Acquire inside their command, so they wait for a
//     foreground slot instead of being skipped.
//     Actions (ci.watch, todo writes, steer.send, restart, create) take no slot.
//
//   - var gitStatusFetch = func(ctx context.Context, cfg *config.Config, s domain.Session) (domain.GitStatus, error)
//     The remote part of fetchGitStatus (today's body after the worktree
//     check). Tests replace it. The git status tick has a per-session
//     in-flight guard: a tick while that session's fetch is running starts
//     nothing.
//
//   - resources.get's JSON gains `unscoped: [{id, name}]` and
//     `gate: {closed, since, memory_some_avg10, memory_full_avg10, slice_over_high}`
//     on server.ResourcesReport (the Go field names are piece B's and D's;
//     this file only decodes the JSON). The header gauge draws in the warning
//     colour while `unscoped` is not empty, and reads `pressure` in the
//     warning colour while `gate.closed` is true.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/usecase"
)

// pbNoRemote makes any ssh or mutagen exec fail at once: no binary on PATH and
// a scratch HOME, so a command these tests start can never reach a host.
func pbNoRemote(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func pbBudget() *usecase.HostBudget {
	at := time.Date(2026, 11, 13, 11, 30, 0, 0, time.UTC)
	return usecase.NewHostBudget(func() time.Time { return at })
}

func pbFillBackground(t *testing.T, b *usecase.HostBudget, host string) []func(bool) {
	t.Helper()
	var rels []func(bool)
	for i := 0; i < usecase.HostBudgetSlots; i++ {
		rel, ok := b.TryAcquire(host)
		if !ok {
			t.Fatalf("background slot %d of %s not granted", i+1, host)
		}
		rels = append(rels, rel)
	}
	return rels
}

func pbFillForeground(t *testing.T, b *usecase.HostBudget, host string) []func(bool) {
	t.Helper()
	var rels []func(bool)
	for i := 0; i < usecase.HostBudgetSlots; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		rel, err := b.Acquire(ctx, host)
		cancel()
		if err != nil {
			t.Fatalf("foreground slot %d of %s: %v", i+1, host, err)
		}
		rels = append(rels, rel)
	}
	return rels
}

func pbWaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// pbRunner runs commands the way the bubbletea runtime does: every leaf in its
// own goroutine, batches unpacked, results collected on msgs.
type pbRunner struct {
	msgs chan tea.Msg
}

func newPBRunner() *pbRunner { return &pbRunner{msgs: make(chan tea.Msg, 4096)} }

func (r *pbRunner) start(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				r.start(c)
			}
			return
		}
		if msg != nil {
			r.msgs <- msg
		}
	}()
}

// pump feeds every result that arrives within d back into the model and runs
// what the model returns, until cond holds or d passes.
func (r *pbRunner) pump(m *Model, d time.Duration, cond func() bool) bool {
	deadline := time.After(d)
	for !cond() {
		select {
		case msg := <-r.msgs:
			_, cmd := m.Update(msg)
			r.start(cmd)
		case <-deadline:
			return cond()
		case <-time.After(5 * time.Millisecond):
		}
	}
	return true
}

// pbTodoClient records to-do reads. Summary can block until released, and it
// tracks how many calls run at once in total and per session.
type pbTodoClient struct {
	mu        sync.Mutex
	block     bool
	release   chan struct{}
	rev       int64
	inflight  int
	peak      int
	perID     map[string]int
	perIDPeak int
	summaries map[string]int
	gets      []string
}

func newPBTodoClient() *pbTodoClient {
	return &pbTodoClient{release: make(chan struct{}), perID: map[string]int{}, summaries: map[string]int{}}
}

func (f *pbTodoClient) Get(_ context.Context, _ config.Remote, id string) (todoSnapshot, error) {
	f.mu.Lock()
	f.gets = append(f.gets, id)
	rev := f.rev
	f.mu.Unlock()
	return newTodoSnapshot(domain.TodoList{SessionID: id, Rev: rev}, server.SteerStatus{}), nil
}

func (f *pbTodoClient) Summary(_ context.Context, _ config.Remote, ids []string) (map[string]todoSummary, error) {
	f.mu.Lock()
	f.inflight++
	if f.inflight > f.peak {
		f.peak = f.inflight
	}
	for _, id := range ids {
		f.perID[id]++
		f.summaries[id]++
		if f.perID[id] > f.perIDPeak {
			f.perIDPeak = f.perID[id]
		}
	}
	block := f.block
	f.mu.Unlock()
	if block {
		<-f.release
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inflight--
	out := map[string]todoSummary{}
	for _, id := range ids {
		f.perID[id]--
		out[id] = todoSummary{Done: 0, Total: 1, Rev: f.rev}
	}
	return out, nil
}

func (f *pbTodoClient) Apply(context.Context, config.Remote, todoOp) error { return nil }

func (f *pbTodoClient) Prompt(context.Context, config.Remote, string, string) (server.SteerStatus, error) {
	return server.SteerStatus{}, nil
}

func (f *pbTodoClient) inFlight() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inflight
}

func (f *pbTodoClient) got(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, g := range f.gets {
		if g == id {
			return true
		}
	}
	return false
}

type pbCIClient struct {
	mu      sync.Mutex
	watches []string
}

func (c *pbCIClient) Get(context.Context, config.Remote, string) (usecase.CIStatus, error) {
	return usecase.CIStatus{}, nil
}

func (c *pbCIClient) SetWatch(_ context.Context, _ config.Remote, id string, on bool) (usecase.CIStatus, error) {
	c.mu.Lock()
	c.watches = append(c.watches, id)
	c.mu.Unlock()
	return usecase.CIStatus{Watch: on}, nil
}

func (c *pbCIClient) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.watches)
}

// pbModel is a dashboard on regent0 with the given sessions, the first one
// selected, a fresh budget, and fakes in place of every to-do and CI call.
func pbModel(t *testing.T, sessions ...domain.Session) (*Model, *pbTodoClient) {
	t.Helper()
	cfg := &config.Config{Remotes: []config.Remote{{Host: "regent0", User: "code"}}}
	m := NewModel(cfg, nil, sessions, nil, nil, nil, nil)
	todo := newPBTodoClient()
	m.todoClient = todo
	m.ciClient = nil
	m.hostBudget = pbBudget()
	m.SetSize(140, 44)
	m.selectFirstSessionRow()
	m.initialLoad = false
	return m, todo
}

func pbSessions(n int) []domain.Session {
	now := time.Now()
	out := make([]domain.Session, n)
	for i := range out {
		id := string(rune('a' + i))
		out[i] = domain.Session{
			ID: id, Name: "s-" + id, Group: "G", RemoteHost: "regent0",
			TerminalName: id, CreatedAt: now.Add(-time.Duration(i) * time.Minute),
			Status: domain.SessionStatusActive,
		}
	}
	return out
}

// A burst of todo_changed events for sessions that are not selected starts at
// most one todo.summary per session and at most four calls on the remote; the
// rest are held, and every one of them runs once slots free.
func TestAcceptanceTodoChangedBurstIsBudgeted(t *testing.T) {
	pbNoRemote(t)
	m, todo := pbModel(t, pbSessions(11)...)
	sel, ok := m.selectedSessionItem()
	if !ok {
		t.Fatal("no session selected")
	}
	var others []string
	for _, s := range m.allSessions {
		if s.ID != sel.session.ID {
			others = append(others, s.ID)
		}
	}
	if len(others) != 10 {
		t.Fatalf("unselected sessions: %v", others)
	}

	todo.block = true
	todo.rev = 2
	r := newPBRunner()
	for rev := int64(1); rev <= 2; rev++ {
		for _, id := range others {
			cmd, claimed := m.applyTodoEvent("regent0", server.SessionEvent{Type: "todo_changed", ID: id, Rev: rev})
			if !claimed {
				t.Fatalf("todo_changed for %s not claimed", id)
			}
			r.start(cmd)
		}
	}

	pbWaitFor(t, "the first summaries to start", func() bool { return todo.inFlight() > 0 })
	time.Sleep(50 * time.Millisecond)
	todo.mu.Lock()
	peak, perIDPeak := todo.peak, todo.perIDPeak
	todo.mu.Unlock()
	if peak > usecase.HostBudgetSlots {
		t.Fatalf("%d todo.summary calls in flight on one remote, want at most %d", peak, usecase.HostBudgetSlots)
	}
	if perIDPeak > 1 {
		t.Fatalf("%d todo.summary calls in flight for one session, want at most 1", perIDPeak)
	}

	allLanded := func() bool {
		if todo.inFlight() != 0 {
			return false
		}
		for _, id := range others {
			if m.todoSummaries[id].Rev != 2 {
				return false
			}
		}
		return true
	}
	deadline := time.Now().Add(5 * time.Second)
	for !allLanded() {
		if time.Now().After(deadline) {
			var missing []string
			for _, id := range others {
				if m.todoSummaries[id].Rev != 2 {
					missing = append(missing, id)
				}
			}
			t.Fatalf("skipped summaries never ran: %v", missing)
		}
		if todo.inFlight() > 0 {
			select {
			case todo.release <- struct{}{}:
			case <-time.After(100 * time.Millisecond):
			}
		}
		r.pump(m, 20*time.Millisecond, func() bool { return false })
	}

	todo.mu.Lock()
	defer todo.mu.Unlock()
	if todo.peak > usecase.HostBudgetSlots {
		t.Fatalf("%d todo.summary calls were in flight at once, want at most %d", todo.peak, usecase.HostBudgetSlots)
	}
	if todo.perIDPeak > 1 {
		t.Fatalf("one session had %d todo.summary calls in flight at once", todo.perIDPeak)
	}
	for _, id := range others {
		if todo.summaries[id] == 0 {
			t.Fatalf("session %s never got its summary", id)
		}
	}
}

// Selecting a session is foreground work: with every background slot taken it
// still loads, and the pane shows the session's last frame, not "Loading...".
func TestAcceptanceSelectionLoadsUnderFullBackgroundBudget(t *testing.T) {
	pbNoRemote(t)
	sessions := pbSessions(2)
	m, todo := pbModel(t, sessions...)
	pbFillBackground(t, m.hostBudget, "regent0")
	m.paneCache = map[string]usecase.PaneCache{sessions[1].ID: {Text: "frame of b"}}

	_, cmd := m.Update(pressKey("down"))
	if sel, ok := m.selectedSessionItem(); !ok || sel.session.ID != sessions[1].ID {
		t.Fatalf("selection did not move to %s", sessions[1].ID)
	}
	if !strings.Contains(m.paneOutput, "frame of b") || strings.Contains(m.paneOutput, "Loading") {
		t.Fatalf("pane while loading = %q, want the last frame", m.paneOutput)
	}
	if cmd == nil {
		t.Fatal("selection under a full background budget started nothing")
	}
	r := newPBRunner()
	r.start(cmd)
	if !r.pump(m, 2*time.Second, func() bool { return todo.got(sessions[1].ID) }) {
		t.Fatal("todo.get for the selected session never ran")
	}
}

// With the foreground slots taken as well, the selection's fetches wait for a
// slot instead of being dropped, the last frame stays on screen meanwhile,
// and the load runs once a foreground slot frees.
func TestAcceptanceSelectionWaitsForForegroundSlot(t *testing.T) {
	pbNoRemote(t)
	sessions := pbSessions(2)
	m, todo := pbModel(t, sessions...)
	pbFillBackground(t, m.hostBudget, "regent0")
	fg := pbFillForeground(t, m.hostBudget, "regent0")
	m.paneCache = map[string]usecase.PaneCache{sessions[1].ID: {Text: "frame of b"}}

	_, cmd := m.Update(pressKey("down"))
	r := newPBRunner()
	r.start(cmd)
	if r.pump(m, 50*time.Millisecond, func() bool { return todo.got(sessions[1].ID) }) {
		t.Fatal("todo.get ran with every foreground slot taken")
	}
	if !strings.Contains(m.paneOutput, "frame of b") {
		t.Fatalf("pane while waiting = %q, want the last frame", m.paneOutput)
	}

	fg[0](false)
	if !r.pump(m, 2*time.Second, func() bool { return todo.got(sessions[1].ID) }) {
		t.Fatal("the waiting selection never loaded after a foreground slot freed")
	}
}

// A git status fetch that stalls holds its session: the next 30s tick starts
// no second fetch, and the tick after the answer starts one again.
func TestAcceptanceStalledGitStatusDoesNotStack(t *testing.T) {
	pbNoRemote(t)
	sessions := pbSessions(1)
	sessions[0].WorktreePath = "/home/code/repos/x@a"
	m, _ := pbModel(t, sessions...)

	var mu sync.Mutex
	calls := 0
	open := make(chan struct{})
	prev := gitStatusFetch
	gitStatusFetch = func(ctx context.Context, _ *config.Config, _ domain.Session) (domain.GitStatus, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		select {
		case <-open:
			return domain.GitStatus{}, nil
		case <-ctx.Done():
			return domain.GitStatus{}, ctx.Err()
		}
	}
	t.Cleanup(func() { gitStatusFetch = prev })
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}

	r := newPBRunner()
	_, cmd := m.Update(gitTickMsg(time.Now()))
	r.start(cmd)
	pbWaitFor(t, "the first git status fetch", func() bool { return count() == 1 })

	_, cmd = m.Update(gitTickMsg(time.Now()))
	r.start(cmd)
	time.Sleep(50 * time.Millisecond)
	if n := count(); n != 1 {
		t.Fatalf("a tick during a stalled git status started %d fetches, want 1", n)
	}

	close(open)
	var answered bool
	deadline := time.After(2 * time.Second)
	for !answered {
		select {
		case msg := <-r.msgs:
			if _, ok := msg.(gitStatusMsg); ok {
				answered = true
			}
			_, c := m.Update(msg)
			r.start(c)
		case <-deadline:
			t.Fatal("the git status answer never arrived")
		}
	}

	_, cmd = m.Update(gitTickMsg(time.Now()))
	r.start(cmd)
	pbWaitFor(t, "a git status fetch after the answer", func() bool { return count() == 2 })
}

// A tick that finds no background slot returns no command and leaves the
// host's in-flight guard unset, so the next tick after a slot frees asks.
func TestAcceptanceSkippedTickLeavesGuardUnset(t *testing.T) {
	m, _ := resourcesModel(t)
	m.hostBudget = pbBudget()
	rels := pbFillBackground(t, m.hostBudget, "devbox")

	if cmd := m.fetchResourcesAll(); cmd != nil {
		t.Fatal("resources.get started with every background slot taken")
	}
	if m.res.inflight["devbox"] {
		t.Fatal("a skipped resources.get left the host marked in flight")
	}

	rels[0](false)
	if cmd := m.fetchResourcesAll(); cmd == nil {
		t.Fatal("resources.get did not start once a slot freed")
	}
}

// An action takes no slot: ci.watch goes out with both pools full.
func TestAcceptanceActionTakesNoSlot(t *testing.T) {
	pbNoRemote(t)
	sessions := pbSessions(1)
	m, _ := pbModel(t, sessions...)
	ci := &pbCIClient{}
	m.ciClient = ci
	pbFillBackground(t, m.hostBudget, "regent0")
	pbFillForeground(t, m.hostBudget, "regent0")

	cmd := m.togglePRWatchCmd(sessions[0])
	if cmd == nil {
		t.Fatal("ci.watch was refused with the budget full")
	}
	r := newPBRunner()
	r.start(cmd)
	pbWaitFor(t, "ci.watch to be sent", func() bool { return ci.count() == 1 })

	if _, ok := m.hostBudget.TryAcquire("regent0"); ok {
		t.Fatal("the action freed or added a background slot")
	}
}

// pbReport decodes a resources.get body the way the TUI receives it.
func pbReport(t *testing.T, body string) server.ResourcesReport {
	t.Helper()
	var r server.ResourcesReport
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	return r
}

// pbWarnSGR is the escape sequence a1Text opens a warning-coloured run with.
func pbWarnSGR(t *testing.T) string {
	t.Helper()
	warn := a1Text("X", a1Wait, false, false)
	dim := a1Text("X", a1Dim, false, false)
	if warn == dim {
		t.Fatal("the test renderer draws no colour; the warning colour cannot be told apart")
	}
	prefix, _, _ := strings.Cut(warn, "X")
	if prefix == "" {
		t.Fatal("the warning colour has no escape prefix")
	}
	return prefix
}

const pbSlice = `"supported":true,"user_slice":{"memory_current":1073741824,"memory_high":25769803776},"sessions":[]`

// The header gauge turns to the warning colour for a host whose report lists
// unscoped sessions, and reads "pressure" in the warning colour while the
// host's pressure gate is closed.
func TestAcceptanceHeaderWarnsOnUnscopedAndPressure(t *testing.T) {
	warn := pbWarnSGR(t)
	m, _ := resourcesModel(t)

	m.applyResourcesMsg(resourcesMsg{host: "devbox", report: pbReport(t, `{`+pbSlice+`}`)})
	calm := m.headerGauge()
	if calm == "" {
		t.Fatal("no gauge for a supported report")
	}
	if strings.Contains(calm, warn) {
		t.Fatalf("a slice at 1G/24G with nothing unscoped is drawn as a warning: %q", calm)
	}
	if strings.Contains(ansi.Strip(calm), "pressure") {
		t.Fatalf("an open gate reads pressure: %q", ansi.Strip(calm))
	}

	m.applyResourcesMsg(resourcesMsg{host: "devbox", report: pbReport(t,
		`{`+pbSlice+`,"unscoped":[{"id":"legacy-1","name":"old agent"}]}`)})
	if got := m.headerGauge(); !strings.Contains(got, warn) {
		t.Fatalf("a report with unscoped sessions is not drawn as a warning: %q", got)
	}

	m.applyResourcesMsg(resourcesMsg{host: "devbox", report: pbReport(t,
		`{`+pbSlice+`,"gate":{"closed":true,"since":"2026-11-13T11:30:00Z","memory_some_avg10":80,"memory_full_avg10":35,"slice_over_high":true}}`)})
	got := m.headerGauge()
	if !strings.Contains(ansi.Strip(got), "pressure") {
		t.Fatalf("a closed gate does not read pressure: %q", ansi.Strip(got))
	}
	if !strings.Contains(got, warn) {
		t.Fatalf("pressure is not in the warning colour: %q", got)
	}
}
