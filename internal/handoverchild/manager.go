package handoverchild

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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

// lookupGrace is how long past Timeout a run waits for the worker lookup to
// succeed. By Timeout the child is done or stopped, so the only thing left to
// wait for is the registry. A fixed grace, rather than a multiple of Timeout,
// keeps a long child timeout from also stretching how long a broken registry
// holds the worker's pointer notes; five minutes still rides out a restarting
// or locked database across dozens of polls.
const lookupGrace = 5 * time.Minute

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
		// The newest render is the fallback if this child fails.
		if len(req.Mechanical) > 0 {
			r.Mechanical, r.MechanicalSHA = string(req.Mechanical), req.MechanicalSHA
			r.Label, r.Abstract = req.Label, req.Abstract
			r.Group, r.IssueKey, r.Repo = w.Group, w.IssueKey, w.RepoName
		}
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
		IssueKey:      w.IssueKey,
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

// LastApplied returns the fingerprints of the worker's newest run when that
// run wrote the worker's handover file: the sha256 of the mechanical render it
// replaced and of the file it wrote. Both are "" otherwise. A new render with
// the same mechanical sha, over a file with the applied sha, means the file on
// disk is still the child's rewrite of the same state.
func (m *Manager) LastApplied(sessionID string) (mechanicalSHA, appliedSHA string) {
	all, err := m.d.Store.List()
	if err != nil {
		return "", ""
	}
	for _, r := range all {
		if r.SessionID != sessionID {
			continue
		}
		if r.Applied {
			return r.MechanicalSHA, r.AppliedSHA
		}
		return "", ""
	}
	return "", ""
}

// Resume picks up runs a previous serve left behind: running ones are watched
// again, starting ones fail with the mechanical fallback after any child they
// created is found, and finished ones that were not cleaned up are cleaned
// now. Cleaned-up runs that LastApplied no longer reads are deleted.
func (m *Manager) Resume(context.Context) {
	all, err := m.d.Store.List()
	if err != nil {
		log.Printf("handoverchild: resume: %v", err)
		return
	}
	m.prune(all)
	for _, r := range all {
		switch {
		case r.Status == StatusRunning:
			m.setActive(r)
			m.goBG(func(ctx context.Context) { m.watch(ctx, r) })
		case r.Status == StatusStarting:
			m.setActive(r)
			m.goBG(func(ctx context.Context) {
				m.adoptOrphan(ctx, r)
				m.retry(ctx, func() bool {
					return m.finish(ctx, r.ID, StatusFailed, "serve restarted before the child started")
				})
			})
		case !r.CleanedUp:
			m.goBG(func(ctx context.Context) {
				if m.retry(ctx, func() bool { return m.releasePointer(ctx, r.ID) }) {
					m.cleanup(ctx, r.ID)
				}
			})
		}
	}
}

func (m *Manager) setActive(r Run) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active[r.SessionID] = r.ID
}

// retry calls fn every Poll until it reports done or ctx ends, and reports
// whether fn got done.
func (m *Manager) retry(ctx context.Context, fn func() bool) bool {
	for !fn() {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(m.d.Poll):
		}
	}
	return true
}

// ChildLister lists the sessions whose parent is parentID. Serve's registry
// implements it; without it a child created just before a crash is left for
// the user to forget.
type ChildLister interface {
	Children(ctx context.Context, parentID string) ([]domain.Session, error)
}

// adoptOrphan records on a starting run the child a crashed serve created but
// did not save, matched by parent, role, and the run's branch or scratch
// directory, so cleanup stops and forgets it.
func (m *Manager) adoptOrphan(ctx context.Context, r Run) {
	lister, ok := m.d.Sessions.(ChildLister)
	if r.ChildID != "" || !ok {
		return
	}
	kids, err := lister.Children(ctx, r.SessionID)
	if err != nil {
		log.Printf("handoverchild: run %s: list children: %v", r.ID, err)
		return
	}
	branch := childBranch(r.ID)
	for _, k := range kids {
		if k.Role != domain.SessionRoleHandover {
			continue
		}
		if k.Branch == branch || (r.Dir != "" && (k.WorktreePath == r.Dir || k.WorkingDirectory == r.Dir)) {
			m.update(r.ID, func(x *Run) { x.ChildID = k.ID })
			return
		}
	}
}

func childBranch(runID string) string {
	return "handover-" + runID[:min(8, len(runID))]
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
		m.retry(ctx, func() bool { return m.finish(ctx, r.ID, StatusFailed, err.Error()) })
		return
	}
	// The child id is saved first, so cleanup can always find the child. A
	// failed save is retried; a crash before it leaves adoptOrphan to Resume.
	id, ok := r.ID, false
	m.retry(ctx, func() bool {
		r, ok = m.update(id, func(x *Run) {
			x.ChildID, x.Agent, x.Status = child.ID, agentName, StatusRunning
		})
		return ok
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
		Branch:           childBranch(r.ID),
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
// out of time. It reports whether the run is finished; a finish that could not
// read the worker is retried on the next poll.
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
	return m.finish(ctx, r.ID, status, "")
}

// childExited treats a SessionEnd report, a dead terminal or a missing
// session as the child having stopped. A registry that cannot answer is not
// an exit.
func (m *Manager) childExited(ctx context.Context, id string) bool {
	s, present, err := m.session(ctx, id)
	if err != nil {
		log.Printf("handoverchild: read child %s: %v", id, err)
		return false
	}
	return !present || s.AgentEnded || !m.d.Terminal.Alive(ctx, s)
}

// session reads one session. A missing session is present false with a nil
// error; any other error means the registry could not answer.
func (m *Manager) session(ctx context.Context, id string) (domain.Session, bool, error) {
	s, err := m.d.Sessions.Get(ctx, id)
	switch {
	case err == nil:
		return s, true, nil
	case errors.Is(err, domain.ErrSessionNotFound):
		return domain.Session{}, false, nil
	default:
		return domain.Session{}, false, err
	}
}

// outputs are what a complete child left in its scratch directory.
type outputs struct {
	handover   string // "" when absent or invalid
	candidates []domain.MemoryCandidate
	err        string
}

func readOutputs(dir string) outputs {
	var o outputs
	raw, err := handover.ReadChildFile(filepath.Join(dir, handover.HandoverFile), handover.MaxChildHandoverBytes)
	if err == nil {
		o.handover, err = handover.ParseChildHandover(raw)
	}
	if err != nil {
		o.err = "child handover rejected: " + err.Error()
	}
	if raw, err := handover.ReadChildFile(filepath.Join(dir, handover.MemoriesFile), handover.MaxChildMemoriesBytes); err == nil {
		o.candidates = handover.ParseChildCandidates(raw)
	}
	return o
}

// applyTimeout bounds one finish attempt. The attempt runs on its own context
// so a Close mid-finish still writes the note and queues the memory job.
const applyTimeout = 30 * time.Second

// finish applies the run's outcome, records it, and cleans up. Only a
// complete run contributes outputs; every other status falls back to the
// mechanical handover. A gone worker gets no file and no steer, but the note
// and the memory job still go out. It reports false when the worker could not
// be read or the outcome could not be saved, and the caller retries. Past the
// lookup bound an unreadable worker fails the run instead of retrying. Every
// step before the save is safe to repeat.
func (m *Manager) finish(ctx context.Context, id string, status Status, errText string) bool {
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), applyTimeout)
	defer cancel()
	r, err := m.d.Store.Get(id)
	if err != nil {
		log.Printf("handoverchild: finish %s: %v", id, err)
		return errors.Is(err, ErrNotFound)
	}
	worker, present, err := m.session(actx, r.SessionID)
	unreadable := false
	if err != nil {
		log.Printf("handoverchild: run %s: read worker: %v", r.ID, err)
		if !m.pastLookupBound(r) {
			return false
		}
		// The worker may still exist, so its pointer is steered below, but
		// no file is written and the mechanical render is the outcome.
		unreadable = true
		status = StatusFailed
		errText = fmt.Sprintf("worker %s could not be read for %s past the run start: %v",
			r.SessionID, m.d.Timeout+lookupGrace, err)
	}
	var out outputs
	if status == StatusComplete {
		out = readOutputs(r.Dir)
		errText = out.err
	}

	applied, appliedSHA := false, ""
	if out.handover != "" && present {
		appliedSHA, applied = m.writeWorkerFile(worker, out.handover)
	}
	body := out.handover
	if body == "" {
		body = strings.TrimPrefix(r.Mechanical, domain.AimanScaffoldHeader)
	}
	m.putNote(actx, r, worker, present, body)
	if m.d.Jobs != nil {
		// The run id makes a retried enqueue a no-op.
		if err := m.d.Jobs.Enqueue(actx, memoryjob.Job{
			ID:         r.ID,
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
		x.AppliedSHA = appliedSHA
		x.FinishedAt = m.d.Now()
	})
	if !ok {
		return false
	}
	if !m.deliverPointer(id, present || unreadable) && applied && (r.Reason == "restart" || r.Reason == "agent_switch") {
		m.steer(r.SessionID, HandoverRefreshedNote)
	}
	m.ledger(actx, r.SessionID, domain.EventHandoverChildFinished, map[string]any{
		"run": r.ID, "agent": r.Agent, "reason": r.Reason, "status": string(status),
		"candidates": len(out.candidates), "error": errText,
	})
	// Cleanup follows ctx so Close does not wait out cleanupWait; an
	// interrupted cleanup is left for Resume.
	m.cleanup(ctx, id)
	m.pruneStore()
	return true
}

// pastLookupBound reports whether the run started more than Timeout plus
// lookupGrace ago, after which an unreadable worker no longer holds it.
func (m *Manager) pastLookupBound(r Run) bool {
	return m.d.Now().Sub(r.StartedAt) > m.d.Timeout+lookupGrace
}

// releasePointer delivers a finished run's held pointer for Resume. It
// reports false, leaving the pointer held, when the worker could not be read
// and the run is inside its lookup bound. Past the bound the pointer is
// steered without knowing whether the worker exists.
func (m *Manager) releasePointer(ctx context.Context, id string) bool {
	r, err := m.d.Store.Get(id)
	if err != nil {
		return errors.Is(err, ErrNotFound)
	}
	if r.HeldPointer == "" || r.PointerReleased {
		return true
	}
	_, present, err := m.session(ctx, r.SessionID)
	if err != nil {
		log.Printf("handoverchild: run %s: read worker: %v", r.ID, err)
		if !m.pastLookupBound(r) {
			return false
		}
		present = true
	}
	m.deliverPointer(id, present)
	return true
}

// deliverPointer queues the run's held pointer once, when the worker is
// present, and reports whether the run held one. A gone worker's pointer is
// marked released without a steer. A crash between the queue and the save
// delivers it twice, which is accepted.
func (m *Manager) deliverPointer(id string, present bool) bool {
	r, err := m.d.Store.Get(id)
	if err != nil || r.HeldPointer == "" || r.PointerReleased {
		return false
	}
	if present {
		m.steer(r.SessionID, r.HeldPointer)
	}
	m.update(id, func(x *Run) { x.PointerReleased = true })
	return true
}

// writeWorkerFile replaces the worker's .aiman_handover.md with the child's
// handover under the scaffold header, when the worker directory exists. It
// returns the sha256 of the bytes written.
func (m *Manager) writeWorkerFile(worker domain.Session, md string) (string, bool) {
	dir := strings.TrimSpace(worker.WorkingDirectory)
	if dir == "" {
		dir = strings.TrimSpace(worker.WorktreePath)
	}
	if dir == "" {
		return "", false
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", false
	}
	if !strings.HasPrefix(md, domain.AimanScaffoldHeader) {
		md = domain.AimanScaffoldHeader + md
	}
	if _, err := fsutil.WriteIfChanged(filepath.Join(dir, domain.AimanHandoverFileName), []byte(md), 0o644); err != nil {
		log.Printf("handoverchild: write handover for %s: %v", worker.ID, err)
		return "", false
	}
	sum := sha256.Sum256([]byte(md))
	return hex.EncodeToString(sum[:]), true
}

// putNote stores the shared-context note handover-<worker id> in the same
// namespace serve's mechanical write uses. A gone worker's namespace comes
// from the fields the run captured at start.
func (m *Manager) putNote(ctx context.Context, r Run, worker domain.Session, present bool, body string) {
	if m.d.Context == nil {
		return
	}
	if !present {
		worker = domain.Session{ID: r.SessionID, Group: r.Group, IssueKey: r.IssueKey, RepoName: r.Repo}
	}
	ns, key := handover.NoteNamespace(worker.Group, worker.IssueKey, worker.RepoName)
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

// pruneStore deletes runs that are cleaned up and not their session's newest.
func (m *Manager) pruneStore() {
	all, err := m.d.Store.List()
	if err != nil {
		log.Printf("handoverchild: prune: %v", err)
		return
	}
	m.prune(all)
}

// prune deletes, from all (newest first), the cleaned-up runs that are not
// their session's newest. The newest stays because LastApplied reads it, and
// a run not yet cleaned up stays for Resume.
func (m *Manager) prune(all []Run) {
	seen := map[string]bool{}
	for _, r := range all {
		newest := !seen[r.SessionID]
		seen[r.SessionID] = true
		if newest || !r.CleanedUp {
			continue
		}
		if err := m.d.Store.Delete(r.ID); err != nil {
			log.Printf("handoverchild: prune run %s: %v", r.ID, err)
		}
	}
}

// cleanup stops the child, waits for its process to go, forgets it and
// removes the scratch directory. If the process outlives cleanupWait, or the
// registry cannot say whether the child exists, the run stays uncleaned so
// Resume retries.
func (m *Manager) cleanup(ctx context.Context, id string) {
	r, err := m.d.Store.Get(id)
	if err != nil {
		return
	}
	if r.ChildID != "" {
		child, present, err := m.session(ctx, r.ChildID)
		if err != nil {
			log.Printf("handoverchild: run %s: read child: %v", r.ID, err)
			return
		}
		if present {
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
