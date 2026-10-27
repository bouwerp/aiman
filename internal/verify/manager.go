package verify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/agent"
)

// ErrReviewerSession refuses to review a reviewer, which would recurse.
var ErrReviewerSession = errors.New("cannot verify a reviewer session")

// defaultPoll is how often a running review's file and process are checked.
const defaultPoll = 5 * time.Second

// cleanupWait is how long cleanup waits for a stopped reviewer to exit before
// leaving the review uncleaned for Resume to retry.
const cleanupWait = 30 * time.Second

// Config is the verify block of serve's config.
type Config struct {
	Agents   []string
	BeforePR bool
	Timeout  time.Duration // zero means DefaultTimeout
	// TestCommand is run on the snapshot to back a pass; empty means no
	// review can pass.
	TestCommand string
	// Reproduce runs TestCommand at the base before a reviewer starts and
	// expects it to fail. Sessions carry no issue type, so unset means off.
	Reproduce *bool
}

// errNotReproduced ends a review whose test command already passes at the
// base: the change has nothing demonstrable to fix.
var errNotReproduced = errors.New("test command passes at the base")

// Sessions is serve's session registry as the manager needs it.
type Sessions interface {
	Get(ctx context.Context, id string) (domain.Session, error)
	CreateChild(ctx context.Context, cfg domain.SessionConfig) (domain.Session, error) // persists the child
	Forget(ctx context.Context, id string) error
}

// Terminal controls a session's terminal process on either backend.
type Terminal interface {
	Alive(ctx context.Context, s domain.Session) bool // false once the terminal process is gone
	Stop(ctx context.Context, s domain.Session) error
}

// Todos is serve's to-do adapter. A Mutate renders the list, publishes
// todo_changed and steers the worker.
type Todos interface {
	Get(sessionID string) (domain.TodoList, error)
	Mutate(ctx context.Context, sessionID string, author domain.TodoAuthor, fn func(*domain.TodoList) error) error
}

// Notifier hears about every finished review: complete, incomplete or timed out.
type Notifier interface {
	ReviewFinished(ctx context.Context, worker domain.Session, r Review)
}

// Deps are the manager's collaborators.
type Deps struct {
	Store     *Store
	Sessions  Sessions
	Terminal  Terminal
	Todos     Todos
	Context   domain.ContextStore
	Notifier  Notifier
	Installed func(string) bool
	Now       func() time.Time // nil means time.Now
	Poll      time.Duration    // zero means 5s
}

// StartRequest asks for a review of one worker session.
type StartRequest struct {
	WorkerID string
	Agent    string // reviewer override; empty picks from Config.Agents
	// IfEnabled returns a disabled review instead of starting one when
	// before_pr is off, so an agent's pre-PR hook can always call verify.
	IfEnabled bool
}

// Manager drives reviews from snapshot to cleanup. Its goroutines run on a
// context it owns, so a review outlives the request that started it.
type Manager struct {
	cfg Config
	d   Deps

	// mu serialises Start, so two calls on one unchanged tree share a review.
	mu sync.Mutex

	bg     context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewManager returns a manager over d.
func NewManager(cfg Config, d Deps) *Manager {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Poll <= 0 {
		d.Poll = defaultPoll
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	bg, cancel := context.WithCancel(context.Background())
	return &Manager{cfg: cfg, d: d, bg: bg, cancel: cancel}
}

// Close stops every launch and watcher and waits for them.
func (m *Manager) Close() {
	m.cancel()
	m.wg.Wait()
}

func (m *Manager) goBG(fn func(ctx context.Context)) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		fn(m.bg)
	}()
}

// Start snapshots the worker's tree and starts a reviewer on it. Only the
// fast part runs here; the worktree, brief and session are set up in the
// background and the review is returned as starting. A tree that already has
// a review, running or finished, gets that review back.
func (m *Manager) Start(ctx context.Context, req StartRequest) (Review, error) {
	worker, err := m.d.Sessions.Get(ctx, req.WorkerID)
	if err != nil {
		return Review{}, err
	}
	if worker.Role == domain.SessionRoleReviewer {
		return Review{}, ErrReviewerSession
	}
	if req.IfEnabled && !m.cfg.BeforePR {
		return Review{WorkerID: worker.ID, Status: StatusDisabled}, nil
	}
	if worker.WorktreePath == "" {
		return Review{}, fmt.Errorf("session %s has no worktree", worker.ID)
	}
	reviewer, err := PickReviewer(m.cfg.Agents, worker.AgentName, req.Agent, m.d.Installed)
	if err != nil {
		return Review{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	snap, err := TakeSnapshot(ctx, worker.WorktreePath)
	if err != nil {
		return Review{}, err
	}
	if existing, ok := m.reviewForTree(worker.ID, snap.Tree); ok {
		return existing, nil
	}

	id := uuid.NewString()
	r := Review{
		ID:           id,
		WorkerID:     worker.ID,
		Agent:        reviewer,
		Tree:         snap.Tree,
		Commit:       snap.Commit,
		RepoWorktree: worker.WorktreePath,
		Worktree:     reviewWorktreePath(worker.WorktreePath, id),
		Dir:          relDir(worker),
		Status:       StatusStarting,
		StartedAt:    m.d.Now(),
	}
	if err := m.d.Store.Put(r); err != nil {
		return Review{}, err
	}
	m.goBG(func(ctx context.Context) { m.launch(ctx, r, worker) })
	return r, nil
}

// reviewForTree finds the worker's review of tree that is still useful: one
// running, or one that completed. Incomplete (the reviewer stopped without
// finishing), failed and timed-out reviews are retried with a fresh reviewer.
func (m *Manager) reviewForTree(workerID, tree string) (Review, bool) {
	all, err := m.d.Store.List()
	if err != nil {
		return Review{}, false
	}
	for _, r := range all {
		if r.WorkerID != workerID || r.Tree != tree {
			continue
		}
		if r.Status.Active() || r.Status == StatusComplete {
			return r, true
		}
	}
	return Review{}, false
}

// Status returns one review.
func (m *Manager) Status(id string) (Review, error) {
	return m.d.Store.Get(id)
}

// BeforePR reports whether agents are asked to pass a review before a PR.
func (m *Manager) BeforePR() bool { return m.cfg.BeforePR }

// ForTree returns the worker's most recent review of tree.
func (m *Manager) ForTree(workerID, tree string) (Review, bool) {
	all, err := m.d.Store.List()
	if err != nil {
		return Review{}, false
	}
	var best Review
	found := false
	for _, r := range all {
		if r.WorkerID == workerID && r.Tree == tree && (!found || r.StartedAt.After(best.StartedAt)) {
			best, found = r, true
		}
	}
	return best, found
}

// LatestFor returns the worker's most recently started review.
func (m *Manager) LatestFor(workerID string) (Review, bool) {
	all, err := m.d.Store.List()
	if err != nil {
		return Review{}, false
	}
	var mine []Review
	for _, r := range all {
		if r.WorkerID == workerID {
			mine = append(mine, r)
		}
	}
	if len(mine) == 0 {
		return Review{}, false
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].StartedAt.After(mine[j].StartedAt) })
	return mine[0], true
}

// launch builds the reviewer's worktree and brief and creates its session.
// Any failure ends the review as failed and removes what was built.
func (m *Manager) launch(ctx context.Context, r Review, worker domain.Session) {
	reviewerSess, err := m.prepare(ctx, &r, worker)
	if errors.Is(err, errNotReproduced) {
		m.notReproduced(ctx, r, worker)
		return
	}
	if err != nil {
		r.Status = StatusFailed
		r.Error = err.Error()
		r.FinishedAt = m.d.Now()
		if rerr := RemoveWorktree(ctx, r.RepoWorktree, r.Worktree); rerr == nil {
			r.CleanedUp = true
		}
		m.save(r)
		return
	}
	r.ReviewerID = reviewerSess.ID
	r.Status = StatusRunning
	m.save(r)
	m.watch(ctx, r)
}

func (m *Manager) prepare(ctx context.Context, r *Review, worker domain.Session) (domain.Session, error) {
	base, err := ResolveBase(ctx, worker.WorktreePath, worker.BaseBranch)
	if err != nil {
		return domain.Session{}, err
	}
	r.BaseRef = base
	r.BaseSHA = base
	if err := m.reproduce(ctx, r); err != nil {
		return domain.Session{}, err
	}
	diff, err := DiffAgainst(ctx, worker.WorktreePath, base, r.Commit, MaxPatchBytes)
	if err != nil {
		return domain.Session{}, err
	}
	if err := AddDetachedWorktree(ctx, worker.WorktreePath, r.Worktree, r.Commit); err != nil {
		return domain.Session{}, err
	}

	// An old or discovered session may have no WorkingDirectory; read the
	// task from the same relative place in its worktree, never serve's cwd.
	task, _ := os.ReadFile(filepath.Join(worker.WorktreePath, r.Dir, domain.AimanTaskFileName))
	todos, _ := m.d.Todos.Get(worker.ID)
	brief := BuildBrief(BriefInput{
		WorkerName: worker.Name,
		BaseRef:    base,
		Commit:     r.Commit,
		Diff:       diff,
		Task:       string(task),
		Todos:      todos.Items,
	})
	dir := filepath.Join(r.Worktree, r.Dir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return domain.Session{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, domain.AimanTaskFileName), []byte(brief), 0o600); err != nil {
		return domain.Session{}, err
	}

	reviewerAgent := domain.Agent{Name: r.Agent, Command: r.Agent}
	if known, ok := agent.FindKnown(r.Agent); ok {
		reviewerAgent = known
	}
	return m.d.Sessions.CreateChild(ctx, domain.SessionConfig{
		Name:             worker.Name + "-review-" + shortHex(),
		Group:            worker.Group,
		ParentID:         worker.ID,
		Role:             domain.SessionRoleReviewer,
		Repo:             domain.Repo{Name: worker.RepoName},
		Directory:        r.Dir,
		Agent:            &reviewerAgent,
		PromptFree:       true,
		PreparedWorktree: r.Worktree,
		InitialPrompt:    InitialPrompt,
		RemoteHost:       worker.RemoteHost,
		SessionBackend:   worker.Backend,
	})
}

// Resume picks up reviews a previous serve left behind: running ones are
// watched again, ones that never got a reviewer fail, and finished ones that
// were not cleaned up are cleaned now.
func (m *Manager) Resume(context.Context) {
	all, err := m.d.Store.List()
	if err != nil {
		log.Printf("verify: resume: %v", err)
		return
	}
	for _, r := range all {
		switch {
		case r.Status == StatusRunning:
			m.goBG(func(ctx context.Context) { m.watch(ctx, r) })
		case r.Status == StatusStarting:
			r.Status = StatusFailed
			r.Error = "serve restarted before the reviewer started"
			r.FinishedAt = m.d.Now()
			m.save(r)
			m.goBG(func(ctx context.Context) { m.cleanup(ctx, r) })
		case r.Status != StatusDisabled && !r.CleanedUp:
			m.goBG(func(ctx context.Context) { m.cleanup(ctx, r) })
		}
	}
}

// reproduce runs the test command at the base when reproduce-first is on.
// It returns errNotReproduced, with the run kept as the review's evidence,
// when the base already passes. A missing command skips the check.
func (m *Manager) reproduce(ctx context.Context, r *Review) error {
	if m.cfg.Reproduce == nil || !*m.cfg.Reproduce {
		return nil
	}
	if strings.TrimSpace(m.cfg.TestCommand) == "" {
		log.Printf("verify: review %s: reproduce is on but verify.test_command is not set; skipping", r.ID)
		return nil
	}
	ev, err := runAt(ctx, *r, r.BaseSHA, "repro", m.cfg.TestCommand, m.d.Now)
	if err != nil {
		return err
	}
	if ev.ExitCode == 0 {
		r.Evidence = ev
		return errNotReproduced
	}
	return nil
}

// notReproduced ends a review without a reviewer: one REQUIRED finding,
// handed to the worker like any other.
func (m *Manager) notReproduced(ctx context.Context, r Review, worker domain.Session) {
	r.Findings = []Finding{{
		ID:       "f1",
		Severity: SeverityRequired,
		Title:    "not reproducible",
		Body:     fmt.Sprintf("`%s` already exits 0 at the base %s, so the change has no failing test to fix.", m.cfg.TestCommand, r.BaseSHA),
	}}
	r.Required, r.Suggestions = 1, 0
	if err := m.addFindingTodos(ctx, r); err != nil {
		log.Printf("verify: review %s: to-dos: %v", r.ID, err)
	}
	r.Status = StatusIncomplete
	r.Error = errNotReproduced.Error()
	r.FinishedAt = m.d.Now()
	if m.d.Notifier != nil {
		m.d.Notifier.ReviewFinished(ctx, worker, r)
	}
	m.save(r)
	m.cleanup(ctx, r)
}

// watch follows a running review until it finishes, then cleans up.
func (m *Manager) watch(ctx context.Context, r Review) {
	t := time.NewTicker(m.d.Poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if m.check(ctx, r) {
			return
		}
	}
}

// check finishes the review when the reviewer wrote the marker, exited, or
// ran out of time. It reports whether the review is finished.
func (m *Manager) check(ctx context.Context, r Review) bool {
	raw, _ := os.ReadFile(filepath.Join(r.Worktree, r.Dir, domain.AimanReviewFileName))
	text := string(raw)
	findings, complete := ParseReview(text)
	var status Status
	switch {
	case complete:
		status = StatusComplete
	case m.reviewerExited(ctx, r.ReviewerID):
		status = StatusIncomplete
	case m.d.Now().Sub(r.StartedAt) > m.cfg.Timeout:
		status = StatusTimedOut
	default:
		return false
	}
	m.finish(ctx, r, status, text, findings)
	return true
}

// reviewerExited treats a SessionEnd report, a dead terminal or a missing
// session as the reviewer having stopped.
func (m *Manager) reviewerExited(ctx context.Context, id string) bool {
	s, err := m.d.Sessions.Get(ctx, id)
	if err != nil {
		return true
	}
	return s.AgentEnded || !m.d.Terminal.Alive(ctx, s)
}

// finish records the outcome, hands REQUIRED findings to the worker, stores
// the review as shared context, notifies, and cleans up. Findings from an
// incomplete review still count: the reviewer wrote them before it stopped.
func (m *Manager) finish(ctx context.Context, r Review, status Status, text string, findings []Finding) {
	worker, err := m.d.Sessions.Get(ctx, r.WorkerID)
	if err != nil {
		worker = domain.Session{ID: r.WorkerID}
	}
	r.Findings = findings
	r.Required, r.Suggestions = 0, 0
	for _, f := range findings {
		switch f.Severity {
		case SeverityRequired:
			r.Required++
		case SeveritySuggestion:
			r.Suggestions++
		}
	}
	if err := m.addFindingTodos(ctx, r); err != nil {
		log.Printf("verify: review %s: to-dos: %v", r.ID, err)
	}
	if m.d.Context != nil && strings.TrimSpace(text) != "" {
		title := fmt.Sprintf("Review of %s: %d required, %d suggestion(s)", worker.Name, r.Required, r.Suggestions)
		_, err := m.d.Context.Put(ctx, domain.ContextEntry{
			Namespace: "group",
			Key:       worker.Group,
			Title:     title,
			Abstract:  title,
			Body:      text,
			SessionID: worker.ID,
			Type:      domain.ContextTypeReview,
			Author:    domain.ContextAuthorVerifier,
		})
		if err != nil {
			log.Printf("verify: review %s: context: %v", r.ID, err)
		}
	}
	if status == StatusComplete {
		ev, err := runEvidence(ctx, r, m.cfg.TestCommand, m.d.Now)
		if err != nil {
			status = StatusIncomplete
			r.Error = err.Error()
		}
		r.Evidence = ev
	}
	r.Status = status
	r.FinishedAt = m.d.Now()
	if m.d.Notifier != nil {
		m.d.Notifier.ReviewFinished(ctx, worker, r)
	}
	m.save(r)
	m.cleanup(ctx, r)
}

// addFindingTodos adds every REQUIRED finding the worker does not already
// have open, in one mutation, so a repeat review does not duplicate them.
func (m *Manager) addFindingTodos(ctx context.Context, r Review) error {
	if r.Required == 0 {
		return nil
	}
	return m.d.Todos.Mutate(ctx, r.WorkerID, domain.AuthorVerifier, func(l *domain.TodoList) error {
		open := map[string]bool{}
		for _, it := range l.Items {
			if it.State == domain.TodoPending || it.State == domain.TodoActive {
				open[it.Text] = true
			}
		}
		for _, f := range r.Findings {
			if f.Severity != SeverityRequired {
				continue
			}
			text := findingTodoText(f)
			if open[text] {
				continue
			}
			open[text] = true
			l.Items = append(l.Items, domain.TodoItem{Text: text, State: domain.TodoPending, Ref: "finding:" + r.ID + "/" + f.ID})
		}
		return nil
	})
}

func findingTodoText(f Finding) string {
	if f.Location == "" {
		return "Review: " + f.Title
	}
	return "Review: " + f.Title + " (" + f.Location + ")"
}

// cleanup stops the reviewer, waits for its process to go, then removes the
// worktree and forgets the session. If the process outlives cleanupWait the
// review stays uncleaned so Resume retries rather than deleting a worktree
// still in use.
func (m *Manager) cleanup(ctx context.Context, r Review) {
	if r.ReviewerID != "" {
		if reviewer, err := m.d.Sessions.Get(ctx, r.ReviewerID); err == nil {
			if err := m.d.Terminal.Stop(ctx, reviewer); err != nil {
				log.Printf("verify: review %s: stop reviewer: %v", r.ID, err)
			}
			if !m.waitGone(ctx, reviewer) {
				return
			}
		}
	}
	if err := RemoveWorktree(ctx, r.RepoWorktree, r.Worktree); err != nil {
		log.Printf("verify: review %s: remove worktree: %v", r.ID, err)
		return
	}
	if r.ReviewerID != "" {
		if err := m.d.Sessions.Forget(ctx, r.ReviewerID); err != nil {
			log.Printf("verify: review %s: forget reviewer: %v", r.ID, err)
		}
	}
	r.CleanedUp = true
	m.save(r)
}

func (m *Manager) waitGone(ctx context.Context, s domain.Session) bool {
	deadline := time.Now().Add(cleanupWait)
	for m.d.Terminal.Alive(ctx, s) {
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(m.d.Poll):
		}
	}
	return true
}

func (m *Manager) save(r Review) {
	if err := m.d.Store.Put(r); err != nil {
		log.Printf("verify: save review %s: %v", r.ID, err)
	}
}

// reviewWorktreePath is the detached worktree beside the worker's:
// <repo>@review-<first 8 of the review id>.
func reviewWorktreePath(workerWorktree, id string) string {
	repo, _, _ := strings.Cut(filepath.Base(workerWorktree), "@")
	return filepath.Join(filepath.Dir(workerWorktree), repo+"@review-"+id[:8])
}

// relDir is the worker's working directory relative to its worktree, "" at
// the root.
func relDir(worker domain.Session) string {
	if worker.WorkingDirectory == "" {
		return ""
	}
	rel, err := filepath.Rel(worker.WorktreePath, worker.WorkingDirectory)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	return rel
}

func shortHex() string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
