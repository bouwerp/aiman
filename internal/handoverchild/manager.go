package handoverchild

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/bouwerp/aiman/internal/contextstore"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/fsutil"
	"github.com/bouwerp/aiman/internal/handover"
	"github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/memoryjob"
)

// HandoverRefreshedNote is queued for a restart or agent_switch worker once a
// child has rewritten its handover, unless a held pointer is released instead.
const HandoverRefreshedNote = "[aiman] Your handover in .aiman_handover.md was rewritten from the full transcript; re-read it."

// ErrHiddenSession refuses a run for a reviewer or handover session.
var ErrHiddenSession = errors.New("hidden sessions get no handover child")

// defaultPoll is how often a running child's marker and process are checked.
const defaultPoll = 5 * time.Second

// cleanupWait is how long cleanup waits for a stopped child to exit before
// leaving the run uncleaned for Resume to retry.
const cleanupWait = 30 * time.Second

// Sessions is serve's session registry. Its method set matches
// verify.Sessions, so serve's adapter serves both managers.
type Sessions interface {
	Get(ctx context.Context, id string) (domain.Session, error)
	CreateChild(ctx context.Context, cfg domain.SessionConfig) (domain.Session, error) // persists the child
	Forget(ctx context.Context, id string) error
}

// Terminal controls a session's terminal process. Its method set matches
// verify.Terminal.
type Terminal interface {
	Alive(ctx context.Context, s domain.Session) bool // false once the terminal process is gone
	Stop(ctx context.Context, s domain.Session) error
}

// Steer queues a steering note for a session with author aiman.
type Steer interface {
	SteerAsAiman(sessionID, text string)
}

// Ledger appends one session_events row with actor aiman. It never fails the
// caller.
type Ledger interface {
	Append(ctx context.Context, sessionID string, kind domain.EventKind, payload map[string]any)
}

// ContextWriter stores the handover note. domain.ContextStore satisfies it.
type ContextWriter interface {
	Put(ctx context.Context, e domain.ContextEntry) (domain.ContextEntry, error)
}

// Jobs queues a memory-candidate job. *memoryjob.Queue satisfies it.
type Jobs interface {
	Enqueue(ctx context.Context, j memoryjob.Job) error
}

// Deps are the manager's collaborators. Steer, Ledger, Context and Jobs may
// be nil.
type Deps struct {
	Store     *Store
	Sessions  Sessions
	Terminal  Terminal
	Steer     Steer
	Ledger    Ledger
	Context   ContextWriter
	Jobs      Jobs
	Installed func(string) bool
	// Candidates lists child agents in preference order for a worker agent;
	// config.(*Config).HandoverCandidates in serve.
	Candidates func(workerAgent string) []string
	// BaseDir holds one scratch directory per run (~/.aiman/handovers).
	BaseDir string
	Now     func() time.Time // nil means time.Now
	Poll    time.Duration    // zero means 5s
	Timeout time.Duration    // zero means config.DefaultHandoverTimeout
}

// StartRequest asks for a handover child for one worker.
type StartRequest struct {
	Worker        domain.Session
	Reason        string
	Mechanical    []byte // the mechanical render just written to the worker
	MechanicalSHA string // sha256 of Mechanical, for LastMechanicalSHA
	Label         string // note title suffix, as handover.Input.Label
	Abstract      string // note abstract, as handover.Abstract
}

// Manager runs at most one handover child per worker session. Its goroutines
// run on a context it owns, so a run outlives the request that started it.
type Manager struct {
	d Deps

	// mu guards active and every read-modify-write of a stored run.
	mu     sync.Mutex
	active map[string]string // worker session id to run id

	bg     context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewManager returns a manager over d.
func NewManager(d Deps) *Manager {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Poll <= 0 {
		d.Poll = defaultPoll
	}
	if d.Timeout <= 0 {
		d.Timeout = config.DefaultHandoverTimeout
	}
	bg, cancel := context.WithCancel(context.Background())
	return &Manager{d: d, active: map[string]string{}, bg: bg, cancel: cancel}
}

// Close stops every launch and watcher and waits for them. Runs left active
// are picked up by Resume in the next serve.
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

// Start records a starting run and launches the child in the background. A
// worker that already has an active run gets that run's id with started
// false; the new reason is recorded on it.
func (m *Manager) Start(_ context.Context, req StartRequest) (runID string, started bool, err error) {
	w := req.Worker
	if strings.TrimSpace(w.ID) == "" {
		return "", false, errors.New("worker session id is required")
	}
	if w.Role.Hidden() {
		return "", false, ErrHiddenSession
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.active[w.ID]; ok {
		r, err := m.d.Store.Get(id)
		if err != nil {
			return "", false, err
		}
		r.LaterReasons = append(r.LaterReasons, req.Reason)
		m.saveLocked(r)
		return id, false, nil
	}
	id := uuid.NewString()
	r := Run{
		ID:            id,
		SessionID:     w.ID,
		Reason:        req.Reason,
		Dir:           filepath.Join(m.d.BaseDir, id),
		Transcript:    w.AgentSessionPath,
		Worktree:      w.WorktreePath,
		Group:         w.Group,
		Repo:          w.RepoName,
		Label:         req.Label,
		Abstract:      req.Abstract,
		Mechanical:    string(req.Mechanical),
		MechanicalSHA: req.MechanicalSHA,
		Status:        StatusStarting,
		StartedAt:     m.d.Now(),
	}
	if err := m.d.Store.Put(r); err != nil {
		return "", false, err
	}
	m.active[w.ID] = id
	m.goBG(func(ctx context.Context) { m.launch(ctx, r, w) })
	return id, true, nil
}

// Active reports whether the worker has a starting or running child.
func (m *Manager) Active(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.active[sessionID]
	return ok
}

// HoldPointer stores text on the worker's active run instead of queueing it;
// the run queues it when it ends. A later pointer replaces an earlier one. It
// reports false, holding nothing, when no run is active.
func (m *Manager) HoldPointer(sessionID, text string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.active[sessionID]
	if !ok {
		return false
	}
	r, err := m.d.Store.Get(id)
	if err != nil {
		return false
	}
	r.HeldPointer = text
	return m.saveLocked(r)
}

// LastMechanicalSHA is the mechanical fingerprint of the worker's newest run
// when that run wrote the worker's handover file, else "". A matching new
// render means the file on disk is a child's rewrite of the same state.
func (m *Manager) LastMechanicalSHA(sessionID string) string {
	all, err := m.d.Store.List()
	if err != nil {
		return ""
	}
	for _, r := range all {
		if r.SessionID != sessionID {
			continue
		}
		if r.Applied {
			return r.MechanicalSHA
		}
		return ""
	}
	return ""
}

// Resume picks up runs a previous serve left behind: running ones are watched
// again, starting ones fail with the mechanical fallback, and finished ones
// that were not cleaned up are cleaned now.
func (m *Manager) Resume(context.Context) {
	all, err := m.d.Store.List()
	if err != nil {
		log.Printf("handoverchild: resume: %v", err)
		return
	}
	for _, r := range all {
		switch {
		case r.Status == StatusRunning:
			m.mu.Lock()
			m.active[r.SessionID] = r.ID
			m.mu.Unlock()
			m.goBG(func(ctx context.Context) { m.watch(ctx, r) })
		case r.Status == StatusStarting:
			m.goBG(func(ctx context.Context) {
				m.finish(ctx, r.ID, StatusFailed, "serve restarted before the child started")
			})
		case !r.CleanedUp:
			m.goBG(func(ctx context.Context) {
				m.releasePointer(ctx, r.ID)
				m.cleanup(ctx, r.ID)
			})
		}
	}
}

// launch builds the scratch directory and creates the child session. A
// failure ends the run as failed with the mechanical fallback.
func (m *Manager) launch(ctx context.Context, r Run, worker domain.Session) {
	child, agentName, err := m.prepare(ctx, r, worker)
	if err != nil {
		if ctx.Err() != nil {
			return // Close; Resume fails the starting run next time
		}
		if agentName != "" {
			m.update(r.ID, func(x *Run) { x.Agent = agentName })
		}
		m.finish(ctx, r.ID, StatusFailed, err.Error())
		return
	}
	r, ok := m.update(r.ID, func(x *Run) {
		x.ChildID, x.Agent, x.Status = child.ID, agentName, StatusRunning
	})
	if !ok {
		return
	}
	m.ledger(ctx, r.SessionID, domain.EventHandoverChildStarted, map[string]any{
		"run": r.ID, "agent": r.Agent, "reason": r.Reason, "child": r.ChildID,
	})
	m.watch(ctx, r)
}

func (m *Manager) prepare(ctx context.Context, r Run, worker domain.Session) (domain.Session, string, error) {
	agentName := m.pickAgent(worker.AgentName)
	if agentName == "" {
		return domain.Session{}, "", errors.New("no handover agent is installed")
	}
	if err := os.MkdirAll(r.Dir, 0o700); err != nil {
		return domain.Session{}, agentName, err
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", r.Dir, "init", "-q").CombinedOutput(); err != nil { //nolint:gosec // r.Dir is the run's scratch directory under BaseDir
		return domain.Session{}, agentName, fmt.Errorf("git init %s: %v: %s", r.Dir, err, strings.TrimSpace(string(out)))
	}
	brief := handover.ChildBrief(handover.BriefInput{
		Dir: r.Dir, Transcript: r.Transcript, Worktree: r.Worktree, Reason: r.Reason,
	})
	if err := os.WriteFile(filepath.Join(r.Dir, handover.BriefFile), []byte(brief), 0o600); err != nil {
		return domain.Session{}, agentName, err
	}
	if err := os.WriteFile(filepath.Join(r.Dir, handover.MechanicalFile), []byte(r.Mechanical), 0o600); err != nil {
		return domain.Session{}, agentName, err
	}
	childAgent := domain.Agent{Name: agentName, Command: agentName}
	if known, ok := agent.FindKnown(agentName); ok {
		childAgent = known
	}
	child, err := m.d.Sessions.CreateChild(ctx, domain.SessionConfig{
		Name:             worker.Name + "-handover-" + shortHex(),
		Group:            worker.Group,
		ParentID:         worker.ID,
		Role:             domain.SessionRoleHandover,
		Branch:           "handover-" + r.ID[:min(8, len(r.ID))],
		Agent:            &childAgent,
		AdHoc:            true,
		PromptFree:       true,
		PreparedWorktree: r.Dir,
		InitialPrompt:    "Read " + filepath.Join(r.Dir, handover.BriefFile) + " and follow it.",
		RemoteHost:       worker.RemoteHost,
		SessionBackend:   worker.Backend,
	})
	return child, agentName, err
}

// pickAgent is the first installed candidate for the worker's agent.
func (m *Manager) pickAgent(workerAgent string) string {
	var names []string
	if m.d.Candidates != nil {
		names = m.d.Candidates(workerAgent)
	} else if workerAgent != "" {
		names = []string{workerAgent}
	}
	for _, name := range names {
		if m.d.Installed != nil && m.d.Installed(name) {
			return name
		}
	}
	return ""
}

// watch follows a running child until it finishes.
func (m *Manager) watch(ctx context.Context, r Run) {
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

// check finishes the run when the child wrote the done marker, exited, or ran
// out of time. It reports whether the run is finished.
func (m *Manager) check(ctx context.Context, r Run) bool {
	var status Status
	switch {
	case fileExists(filepath.Join(r.Dir, handover.DoneFile)):
		status = StatusComplete
	case m.childExited(ctx, r.ChildID):
		status = StatusIncomplete
	case m.d.Now().Sub(r.StartedAt) > m.d.Timeout:
		status = StatusTimedOut
	default:
		return false
	}
	m.finish(ctx, r.ID, status, "")
	return true
}

// childExited treats a SessionEnd report, a dead terminal or a missing
// session as the child having stopped.
func (m *Manager) childExited(ctx context.Context, id string) bool {
	s, err := m.d.Sessions.Get(ctx, id)
	if err != nil {
		return true
	}
	return s.AgentEnded || !m.d.Terminal.Alive(ctx, s)
}

// outputs are what a complete child left in its scratch directory.
type outputs struct {
	handover   string // "" when absent or invalid
	candidates []domain.MemoryCandidate
	err        string
}

func readOutputs(dir string) outputs {
	var o outputs
	raw, err := os.ReadFile(filepath.Join(dir, handover.HandoverFile))
	if err == nil {
		o.handover, err = handover.ParseChildHandover(raw)
	}
	if err != nil {
		o.err = "child handover rejected: " + err.Error()
	}
	if raw, err := os.ReadFile(filepath.Join(dir, handover.MemoriesFile)); err == nil {
		o.candidates = handover.ParseChildCandidates(raw)
	}
	return o
}

// finish applies the run's outcome, records it, and cleans up. Only a
// complete run contributes outputs; every other status falls back to the
// mechanical handover. A gone worker gets no file and no steer, but the note
// and the memory job still go out.
func (m *Manager) finish(ctx context.Context, id string, status Status, errText string) {
	r, err := m.d.Store.Get(id)
	if err != nil {
		log.Printf("handoverchild: finish %s: %v", id, err)
		return
	}
	var out outputs
	if status == StatusComplete {
		out = readOutputs(r.Dir)
		errText = out.err
	}
	worker, werr := m.d.Sessions.Get(ctx, r.SessionID)
	workerExists := werr == nil

	applied := false
	if out.handover != "" && workerExists {
		applied = m.writeWorkerFile(worker, out.handover)
	}
	body := out.handover
	if body == "" {
		body = strings.TrimPrefix(r.Mechanical, domain.AimanScaffoldHeader)
	}
	m.putNote(ctx, r, worker, workerExists, body)
	if m.d.Jobs != nil {
		if err := m.d.Jobs.Enqueue(ctx, memoryjob.Job{
			SessionID:  r.SessionID,
			Reason:     r.Reason,
			Group:      r.Group,
			Repo:       r.Repo,
			Label:      r.Label,
			Source:     body,
			Candidates: out.candidates,
		}); err != nil {
			log.Printf("handoverchild: run %s: memory job: %v", r.ID, err)
		}
	}

	r, ok := m.update(id, func(x *Run) {
		x.Status, x.Error, x.Applied, x.Candidates = status, errText, applied, len(out.candidates)
		x.FinishedAt = m.d.Now()
	})
	if !ok {
		return
	}
	if !m.releasePointer(ctx, id) && applied && (r.Reason == "restart" || r.Reason == "agent_switch") {
		m.steer(r.SessionID, HandoverRefreshedNote)
	}
	m.ledger(ctx, r.SessionID, domain.EventHandoverChildFinished, map[string]any{
		"run": r.ID, "agent": r.Agent, "reason": r.Reason, "status": string(status),
		"candidates": len(out.candidates), "error": errText,
	})
	m.cleanup(ctx, id)
}

// releasePointer queues the run's held pointer once, when the worker still
// exists, and reports whether the run held one. A crash between the queue and
// the save delivers it twice, which is accepted.
func (m *Manager) releasePointer(ctx context.Context, id string) bool {
	r, err := m.d.Store.Get(id)
	if err != nil || r.HeldPointer == "" || r.PointerReleased {
		return false
	}
	if _, err := m.d.Sessions.Get(ctx, r.SessionID); err == nil {
		m.steer(r.SessionID, r.HeldPointer)
	}
	m.update(id, func(x *Run) { x.PointerReleased = true })
	return true
}

// writeWorkerFile replaces the worker's .aiman_handover.md with the child's
// handover under the scaffold header, when the worker directory exists.
func (m *Manager) writeWorkerFile(worker domain.Session, md string) bool {
	dir := strings.TrimSpace(worker.WorkingDirectory)
	if dir == "" {
		dir = strings.TrimSpace(worker.WorktreePath)
	}
	if dir == "" {
		return false
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return false
	}
	if !strings.HasPrefix(md, domain.AimanScaffoldHeader) {
		md = domain.AimanScaffoldHeader + md
	}
	if _, err := fsutil.WriteIfChanged(filepath.Join(dir, domain.AimanHandoverFileName), []byte(md), 0o644); err != nil {
		log.Printf("handoverchild: write handover for %s: %v", worker.ID, err)
		return false
	}
	return true
}

// putNote stores the shared-context note handover-<worker id>, the same id
// serve's mechanical write uses.
func (m *Manager) putNote(ctx context.Context, r Run, worker domain.Session, workerExists bool, body string) {
	if m.d.Context == nil {
		return
	}
	if !workerExists {
		worker = domain.Session{ID: r.SessionID, Group: r.Group, RepoName: r.Repo}
	}
	ns, key := noteNamespace(worker)
	_, err := m.d.Context.Put(ctx, domain.ContextEntry{
		ID:        "handover-" + contextstore.SafeKey(r.SessionID),
		Namespace: ns,
		Key:       key,
		Title:     "Handover: " + r.Label,
		Abstract:  r.Abstract,
		Body:      body,
		SessionID: r.SessionID,
		Type:      domain.ContextTypeHandover,
		Author:    domain.ContextAuthorAiman,
	})
	if err != nil {
		log.Printf("handoverchild: run %s: context note: %v", r.ID, err)
	}
}

// noteNamespace matches serve's handover note: group, else the issue key as a
// group, else the repo, else the host note.
func noteNamespace(sess domain.Session) (string, string) {
	if g := strings.TrimSpace(sess.Group); g != "" {
		return domain.ContextNSGroup, g
	}
	if issue := strings.TrimSpace(sess.IssueKey); issue != "" {
		return domain.ContextNSGroup, issue
	}
	if repo := strings.TrimSpace(sess.RepoName); repo != "" {
		return domain.ContextNSRepo, repo
	}
	return domain.ContextNSHost, "host"
}

// cleanup stops the child, waits for its process to go, forgets it and
// removes the scratch directory. If the process outlives cleanupWait the run
// stays uncleaned so Resume retries.
func (m *Manager) cleanup(ctx context.Context, id string) {
	r, err := m.d.Store.Get(id)
	if err != nil {
		return
	}
	if r.ChildID != "" {
		if child, err := m.d.Sessions.Get(ctx, r.ChildID); err == nil {
			if err := m.d.Terminal.Stop(ctx, child); err != nil {
				log.Printf("handoverchild: run %s: stop child: %v", r.ID, err)
			}
			if !m.waitGone(ctx, child) {
				return
			}
			if err := m.d.Sessions.Forget(ctx, r.ChildID); err != nil {
				log.Printf("handoverchild: run %s: forget child: %v", r.ID, err)
				return
			}
		}
	}
	if !m.inBase(r.Dir) {
		log.Printf("handoverchild: run %s: refusing to remove %q outside %q", r.ID, r.Dir, m.d.BaseDir)
		return
	}
	if err := os.RemoveAll(r.Dir); err != nil {
		log.Printf("handoverchild: run %s: remove scratch: %v", r.ID, err)
		return
	}
	m.update(id, func(x *Run) { x.CleanedUp = true })
}

// inBase reports whether dir is a direct child of BaseDir.
func (m *Manager) inBase(dir string) bool {
	base := filepath.Clean(m.d.BaseDir)
	return m.d.BaseDir != "" && dir != "" && filepath.Dir(filepath.Clean(dir)) == base
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

// update applies fn to the stored run under the manager lock and saves it.
// A run that left the active states also leaves the active index.
func (m *Manager) update(id string, fn func(*Run)) (Run, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.d.Store.Get(id)
	if err != nil {
		log.Printf("handoverchild: update %s: %v", id, err)
		return Run{}, false
	}
	fn(&r)
	if !r.Status.Active() && m.active[r.SessionID] == r.ID {
		delete(m.active, r.SessionID)
	}
	return r, m.saveLocked(r)
}

func (m *Manager) saveLocked(r Run) bool {
	if err := m.d.Store.Put(r); err != nil {
		log.Printf("handoverchild: save run %s: %v", r.ID, err)
		return false
	}
	return true
}

func (m *Manager) steer(sessionID, text string) {
	if m.d.Steer != nil {
		m.d.Steer.SteerAsAiman(sessionID, text)
	}
}

func (m *Manager) ledger(ctx context.Context, sessionID string, kind domain.EventKind, payload map[string]any) {
	if m.d.Ledger != nil {
		m.d.Ledger.Append(ctx, sessionID, kind, payload)
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func shortHex() string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
