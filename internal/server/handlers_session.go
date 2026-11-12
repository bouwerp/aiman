package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/coordinator"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/local"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
	"github.com/bouwerp/aiman/internal/limits"
	"github.com/bouwerp/aiman/internal/notify"
	"github.com/bouwerp/aiman/internal/pane"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/todo"
	"github.com/bouwerp/aiman/internal/usecase"
)

type SessionInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Group        string `json:"group"`
	ParentID     string `json:"parent_id,omitempty"`
	Role         string `json:"role,omitempty"`
	IssueKey     string `json:"issue_key,omitempty"`
	Branch       string `json:"branch,omitempty"`
	BaseBranch   string `json:"base_branch,omitempty"`
	RepoName     string `json:"repo_name,omitempty"`
	TerminalName string `json:"tmux_session,omitempty"`
	// Backend is "pty", or "tmux" for a legacy row aiman no longer runs.
	// Clients use it to show and refuse legacy rows.
	Backend          string `json:"backend,omitempty"`
	WorktreePath     string `json:"worktree_path,omitempty"`
	WorkingDirectory string `json:"working_directory,omitempty"`
	AgentName        string `json:"agent_name,omitempty"`
	// LaunchModel and LaunchEffort are the per-launch model and effort the
	// session was created with, so the laptop relaunches with them too.
	LaunchModel  string `json:"launch_model,omitempty"`
	LaunchEffort string `json:"launch_effort,omitempty"`
	// Tier is the model tier the session was created from, after fallback.
	Tier string `json:"tier,omitempty"`
	// ReviewPR is "owner/repo#n" for a session reviewing that pull request.
	ReviewPR string `json:"review_pr,omitempty"`
	// FixPR is "owner/repo#n" for a fixer session pushing to that PR.
	FixPR            string `json:"fix_pr,omitempty"`
	AgentSessionID   string `json:"agent_session_id,omitempty"`
	AgentSessionPath string `json:"agent_session_path,omitempty"`
	Title            string `json:"title,omitempty"`
	Status           string `json:"status,omitempty"`
	State            string `json:"state,omitempty"`
	StateConfidence  string `json:"state_confidence,omitempty"`
	StateMessage     string `json:"state_message,omitempty"`
	StateSource      string `json:"state_source,omitempty"`
	Ended            bool   `json:"ended,omitempty"`
	// Unprotected means before_pr is on but the agent has no pre-tool hook
	// to block gh pr create, so the gate is advisory only.
	Unprotected bool `json:"unprotected,omitempty"`
	// Tunnels are laptop↔remote forwards; review.debug fills them with the
	// host-published debug ports so the TUI can forward them.
	Tunnels []domain.Tunnel `json:"tunnels,omitempty"`
	// Limits is what is left of the allowance of the provider this session's
	// agent runs on. Only session.get fills it, and it is absent when the agent
	// has no source or the source has not finished its first run.
	Limits *limits.Reading `json:"limits,omitempty"`
	Self   bool            `json:"self"`
}

func sessionInfo(s domain.Session, callerID string) SessionInfo {
	return SessionInfo{
		ID:               s.ID,
		Name:             s.Name,
		Group:            s.Group,
		ParentID:         s.ParentID,
		Role:             string(s.Role),
		BaseBranch:       s.BaseBranch,
		IssueKey:         s.IssueKey,
		Branch:           s.Branch,
		RepoName:         s.RepoName,
		TerminalName:     s.TerminalName,
		Backend:          s.Backend,
		WorktreePath:     s.WorktreePath,
		WorkingDirectory: s.WorkingDirectory,
		AgentName:        s.AgentName,
		LaunchModel:      s.LaunchModel,
		LaunchEffort:     s.LaunchEffort,
		Tier:             s.Tier,
		ReviewPR:         s.ReviewPR,
		FixPR:            s.FixPR,
		AgentSessionID:   s.AgentSessionID,
		AgentSessionPath: s.AgentSessionPath,
		Title:            s.AgentTitle,
		Status:           string(s.Status),
		State:            string(domain.AgentStateUnknown),
		Ended:            s.AgentEnded,
		Tunnels:          s.Tunnels,
		Self:             callerID != "" && callerID == s.ID,
	}
}

func (s *Server) liveSession(ctx context.Context, sess domain.Session) domain.Session {
	if s.repo == nil || sess.ID == "" {
		return sess
	}
	got, err := s.repo.Get(ctx, sess.ID)
	if err != nil {
		return sess
	}
	return *got
}

// freshRows is the rows of list that match, with their hook fields re-read
// by id. The cached list is current for identity columns only; a caller that
// branches on a hook field (AgentEnded) narrows by identity first and reads
// those rows here.
func (s *Server) freshRows(ctx context.Context, list []domain.Session, match func(domain.Session) bool) []domain.Session {
	var out []domain.Session
	for _, c := range list {
		if match(c) {
			out = append(out, s.freshHookFields(ctx, c))
		}
	}
	return out
}

// freshHookFields is sess with the hook-written fields of its stored row,
// which a hook report saves without invalidating the session list cache.
// Identity and the live pty merge stay as given.
func (s *Server) freshHookFields(ctx context.Context, sess domain.Session) domain.Session {
	got := s.liveSession(ctx, sess)
	sess.AgentEnded = got.AgentEnded
	sess.AgentTitle = got.AgentTitle
	sess.AgentSessionID = got.AgentSessionID
	sess.AgentSessionPath = got.AgentSessionPath
	sess.HookState = got.HookState
	sess.HookStateMessage = got.HookStateMessage
	sess.HookStateSource = got.HookStateSource
	sess.HookStateSeq = got.HookStateSeq
	sess.HookStateAt = got.HookStateAt
	return sess
}

func (s *Server) decorateState(ctx context.Context, info *SessionInfo, sess domain.Session) {
	live := s.liveSession(ctx, sess)
	info.Title = live.AgentTitle
	info.Ended = live.AgentEnded
	info.Unprotected = s.verifier != nil && s.verifier.BeforePR() && live.ReviewPR == "" && live.FixPR == "" && !agenthook.GuardsPRCreate(live.AgentName)
	st, conf := s.classify(ctx, live)
	info.State = string(st)
	info.StateConfidence = confidenceName(conf)
	if live.HookStateMessage != "" && st == domain.AgentStateWaitingInput {
		info.StateMessage = live.HookStateMessage
	}
	if live.HookStateSource != "" && conf == pane.High {
		info.StateSource = live.HookStateSource
	}
}

// classify answers what a session is doing, and records a pane the rules could
// not read. The sweep calls sampleState instead, which writes nothing.
func (s *Server) classify(ctx context.Context, sess domain.Session) (domain.AgentState, pane.Confidence) {
	sample := s.sampleState(ctx, sess)
	if sample.uncertain != nil {
		s.recordUncertainPane(ctx, sample.live, sample.screen, *sample.uncertain)
	}
	return sample.State, sample.Confidence
}

// stateSample is one reading of a session, with no side effects behind it.
type stateSample struct {
	State      domain.AgentState
	Confidence pane.Confidence
	// Reason is one of the notify package's fixed reasons on a waiting_input, so
	// it is safe to send anywhere.
	Reason string

	// live is the session the sample read, and screen its capture when one was
	// taken. uncertain is the rules' low-confidence verdict for classify to record.
	live      domain.Session
	screen    string
	uncertain *pane.Result
}

// sampleState reads a session's state: an ended agent first, then a fresh hook
// report, then the screen rules. A fresh working report still loses to a menu on
// screen, because an agent asks for permission in the middle of a turn. A fresh
// idle report is never overruled, which shields ordinary text from a false match.
func (s *Server) sampleState(ctx context.Context, sess domain.Session) stateSample {
	return s.sampleLive(s.liveSession(ctx, sess), time.Now())
}

// sampleLive is sampleState for a row already read by id, judging the hook
// report's freshness at now. The sweep reads each row once and uses it both to
// pick the capture group and to sample, so the two cannot disagree.
func (s *Server) sampleLive(live domain.Session, now time.Time) stateSample {
	sample := stateSample{live: live}
	if st, ok := agenthook.ResolveHookState(live, now); ok {
		sample.State, sample.Confidence = st, pane.High
		switch {
		case live.AgentEnded:
		case st == domain.AgentStateWaitingInput:
			sample.Reason = notify.ReasonConfirmation
		case st == domain.AgentStateWorking:
			s.applyMenuOverride(&sample)
		}
		return sample
	}
	s.sampleRules(&sample)
	return sample
}

// applyMenuOverride turns a working sample into waiting_input when a menu is on
// screen. With no screen to read, the hook's answer stands.
func (s *Server) applyMenuOverride(sample *stateSample) {
	screen, ok := s.captureLive(sample.live)
	if !ok {
		return
	}
	sample.screen = screen
	if reason, open := pane.MenuOpen(screen); open {
		sample.State, sample.Reason = domain.AgentStateWaitingInput, notifyReason(reason)
	}
}

// sampleRules answers from the screen rules, for a session with no fresh hook
// report.
func (s *Server) sampleRules(sample *stateSample) {
	sample.State, sample.Confidence = domain.AgentStateUnknown, pane.Low
	live := sample.live
	// pane.Classify reads a rendered screen, not the raw spool.
	screen, ok := s.captureLive(live)
	if !ok {
		return
	}
	sample.screen = screen
	// A turn outlasts the hook's TTL, so a menu in a long turn has only the stale
	// working report above it, and can sit above the rules' tail.
	if agenthook.TurnOpen(live) {
		if reason, open := pane.MenuOpen(screen); open {
			sample.State, sample.Confidence, sample.Reason = domain.AgentStateWaitingInput, pane.High, notifyReason(reason)
			return
		}
	}
	obs := pane.Observation{Pane: screen, SinceOutput: -1, SinceTitleChange: -1}
	// The holder publishes when output last arrived and when the agent last
	// changed its terminal title. Both are certain, where the screen is
	// evidence to be interpreted.
	if info, ierr := s.pty.Get(live.ID); ierr == nil {
		now := time.Now()
		if !info.LastOutput.IsZero() && !now.Before(info.LastOutput) {
			obs.SinceOutput = now.Sub(info.LastOutput)
		}
		if !info.TitleChanged.IsZero() && !now.Before(info.TitleChanged) {
			obs.SinceTitleChange = now.Sub(info.TitleChanged)
		}
	}
	r := pane.Classify(obs)
	// The rules saw recent output and nothing they recognise. With a turn still
	// open that is the turn running; when the output stops, the rules' silence
	// check answers instead, so an agent that died without a stop hook still
	// settles.
	if r.State == domain.AgentStateUnknown && obs.SinceOutput >= 0 && agenthook.TurnOpen(live) {
		sample.State, sample.Confidence = domain.AgentStateWorking, pane.High
		return
	}
	sample.State, sample.Confidence = r.State, r.Confidence
	if r.State == domain.AgentStateWaitingInput {
		sample.Reason = notifyReason(r.Reason)
	}
	if r.Confidence != pane.High {
		sample.uncertain = &r
	}
}

// captureLive renders a session's screen from the PTY runtime in this process,
// through captureScreen.
func (s *Server) captureLive(sess domain.Session) (string, bool) {
	if s.pty == nil || sess.IsLegacyTmux() {
		return "", false
	}
	screen, err := s.captureScreen(sess.ID)
	if err != nil {
		return "", false
	}
	return screen, true
}

// captureScreen is CaptureScreen inside the capture semaphore. Every capture
// serve makes for itself goes through it.
func (s *Server) captureScreen(id string) (string, error) {
	defer s.admission().captureSlot()()
	return s.pty.CaptureScreen(id)
}

// confidenceName is a confidence as the API spells it.
func confidenceName(c pane.Confidence) string {
	if c == pane.High {
		return "high"
	}
	return "low"
}

// recordUncertainPane appends pane.uncertain when the rules classified a
// captured pane at low confidence. Only the hash of the cleaned pane is
// stored, never its text, and an unchanged pane is recorded once.
func (s *Server) recordUncertainPane(ctx context.Context, sess domain.Session, text string, r pane.Result) {
	if r.Confidence == pane.High {
		return
	}
	sum := sha256.Sum256([]byte(pane.Clean(text)))
	hash := hex.EncodeToString(sum[:])
	if prev, loaded := s.uncertainPanes.Swap(sess.ID, hash); loaded && prev == hash {
		return
	}
	s.appendEvent(ctx, sess.ID, domain.EventActorAiman, domain.EventPaneUncertain, map[string]string{
		"agent":     sess.AgentName,
		"backend":   sess.Backend,
		"state":     string(r.State),
		"pane_hash": hash,
	})
}

func parseUntil(v string) domain.AgentState {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "blocked" {
		return domain.AgentStateWaitingInput
	}
	return domain.AgentState(v)
}

// liveSessions enumerates the sessions actually running on this host.
//
// serve's own database is not a usable index of them: sessions are created by
// the laptop TUI, which persists to the *laptop's* database, so the remote's
// copy stays empty and a DB-only listing reported no sessions at all while six
// were running. serve owns the PTY runtime, so it asks it directly.
func (s *Server) liveSessions(context.Context) []domain.Session {
	if s.pty == nil {
		return nil
	}
	var out []domain.Session
	for _, info := range s.pty.List() {
		id := strings.TrimSpace(info.ID)
		if id == "" || info.Status != ptyruntime.StatusRunning {
			continue
		}
		out = append(out, domain.Session{
			ID:               id,
			TerminalName:     info.Name,
			Backend:          domain.BackendPTY,
			WorkingDirectory: info.Dir,
			ParentID:         info.ParentID,
			Role:             domain.SessionRole(info.Role),
			Status:           domain.SessionStatusActive,
		})
	}
	return out
}

// mergeLiveSessions folds what is running on this host into the stored list.
// Stored records win for identity the host cannot know (name, group, issue,
// branch, repo); liveness and terminal facts come from the host. A stored
// legacy tmux row is inactive: aiman cannot reach its terminal. A live PTY
// with the same id means it already moved to pty.
func mergeLiveSessions(stored, live []domain.Session) []domain.Session {
	byID := make(map[string]int, len(stored))
	for i := range stored {
		byID[stored[i].ID] = i
		if stored[i].IsLegacyTmux() {
			stored[i].Status = domain.SessionStatusInactive
		}
	}
	out := stored
	for _, l := range live {
		if i, ok := byID[l.ID]; ok {
			if l.TerminalName != "" {
				out[i].TerminalName = l.TerminalName
			}
			if l.WorkingDirectory != "" {
				out[i].WorkingDirectory = l.WorkingDirectory
			}
			if l.Backend != "" {
				out[i].Backend = l.Backend
			}
			if out[i].Role == "" {
				out[i].Role = l.Role
			}
			if out[i].ParentID == "" {
				out[i].ParentID = l.ParentID
			}
			out[i].Status = l.Status
			continue
		}
		byID[l.ID] = len(out)
		out = append(out, l)
	}
	return out
}

// sessionListTTL bounds how long loadSessions serves the stored list from its
// cache. Writes by this process invalidate it at once through the
// repository's OnChange; the TTL is what brings in a write by another process
// (aiman-trigger).
var sessionListTTL = 2 * time.Second

// changeNotifier is a repository that reports committed writes. serve finds it
// by interface, so a wrapper embedding the sqlite repository still counts.
type changeNotifier interface {
	OnChange(func(sqlite.ChangeKind))
}

// sessionListCache is the stored session list, read through. It is used only
// when the repository reports changes; without that a cached list could hide
// this process's own writes.
type sessionListCache struct {
	mu      sync.Mutex
	enabled bool
	gen     uint64 // bumped by invalidate; a read started before it is not stored
	ok      bool
	at      time.Time
	rows    []domain.Session
}

func (c *sessionListCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.ok = false
	c.rows = nil
}

// watchSessionList subscribes the cache to the repository's identity changes.
func (s *Server) watchSessionList(repo any) {
	n, ok := repo.(changeNotifier)
	if !ok {
		return
	}
	s.sessList.enabled = true
	n.OnChange(func(k sqlite.ChangeKind) {
		if k == sqlite.ChangeIdentity {
			s.sessList.invalidate()
		}
	})
}

// storedSessions returns a copy of the stored rows, from the cache while it is
// younger than sessionListTTL and no identity change has landed since. The
// copy is shallow: pointer, slice and map fields (Tunnels, AWSConfig,
// AutonomousConfig) are shared with the cache, so callers must not mutate
// them in place; assigning a new value to a field is fine.
func (s *Server) storedSessions(ctx context.Context) ([]domain.Session, error) {
	c := &s.sessList
	c.mu.Lock()
	if !c.enabled {
		c.mu.Unlock()
		return s.repo.List(ctx)
	}
	if c.ok && time.Since(c.at) < sessionListTTL {
		out := slices.Clone(c.rows)
		c.mu.Unlock()
		return out, nil
	}
	gen := c.gen
	c.mu.Unlock()
	list, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.gen == gen {
		c.rows, c.at, c.ok = list, time.Now(), true
	}
	c.mu.Unlock()
	return slices.Clone(list), nil
}

// loadSessions is the stored list with what runs here folded in. It never
// writes: names and groups are backfilled at startup (backfillNames) and by
// the paths that create rows.
func (s *Server) loadSessions(ctx context.Context) ([]domain.Session, error) {
	if s.repo == nil {
		return nil, nil
	}
	list, err := s.storedSessions(ctx)
	if err != nil {
		return nil, err
	}

	// Fold in what is running here. Discovered sessions absent from the
	// local database are labelled in memory only; they become rows when the
	// laptop registers them (session.register) or an agent hook reports for
	// them, so identity is never invented in two places.
	stored := len(list)
	list = mergeLiveSessions(list, s.liveSessions(ctx))
	for i := stored; i < len(list); i++ {
		labelDiscovered(&list[i], list[:i])
	}

	sort.Slice(list, func(i, j int) bool {
		if list[i].Group != list[j].Group {
			return list[i].Group < list[j].Group
		}
		return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
	})
	return list, nil
}

// BackfillNames names and groups every stored row missing either, once at
// startup, so the read path never has to. A failed save is logged and the pass
// goes on; the first error is returned.
func (s *Server) BackfillNames(ctx context.Context) error {
	if s.repo == nil {
		return nil
	}
	list, err := s.repo.List(ctx)
	if err != nil {
		return err
	}
	var first error
	for i := range list {
		if list[i].Name != "" && list[i].Group != "" {
			continue
		}
		s.backfill(&list[i], list)
		if err := s.repo.Save(ctx, &list[i]); err != nil {
			log.Printf("backfill names: %s: %v", list[i].ID, err)
			if first == nil {
				first = err
			}
		}
	}
	return first
}

// backfillNew names and groups a row about to be created, against the rows
// already stored.
func (s *Server) backfillNew(ctx context.Context, sess *domain.Session) {
	if sess.Name != "" && sess.Group != "" {
		return
	}
	existing, err := s.storedSessions(ctx)
	if err != nil {
		existing = nil
	}
	s.backfill(sess, existing)
}

// labelDiscovered names a session found running on this host but absent from
// its database.
//
// It deliberately does not use AssignSessionName: that serves the creation
// flow, where the first session earns the friendly name "impl" and a
// long terminal name fails ValidateSessionName and is discarded. That turned six
// distinct sessions into impl, impl-2 … impl-6 — unidentifiable, and useless
// for addressing a specific sibling. The terminal name is the label
// already shown in the dashboard, so it is what an agent should be able to use.
func labelDiscovered(sess *domain.Session, others []domain.Session) {
	if sess.IssueKey == "" {
		sess.IssueKey = domain.ExtractKey(sess.TerminalName)
	}
	if sess.Group == "" {
		sess.Group = domain.AssignSessionGroup("", sess.IssueKey, sess.RepoName, false)
	}
	if sess.Name == "" {
		sess.Name = terminalLabel(*sess, others)
	}
}

// terminalLabel is sess's terminal name, else its id, suffixed until no other
// session has it. Names address sessions, so they must stay distinct even if
// two hosts ever present the same terminal name.
func terminalLabel(sess domain.Session, others []domain.Session) string {
	name := strings.TrimSpace(sess.TerminalName)
	if name == "" {
		name = sess.ID
	}
	base := name
	for n := 2; domain.NameTaken(others, name); n++ {
		name = fmt.Sprintf("%s-%d", base, n)
	}
	return name
}

func (s *Server) backfill(sess *domain.Session, existing []domain.Session) {
	others := make([]domain.Session, 0, len(existing))
	for _, e := range existing {
		if e.ID != sess.ID {
			others = append(others, e)
		}
	}
	if sess.Group == "" {
		sess.Group = domain.AssignSessionGroup("", sess.IssueKey, sess.RepoName, false)
	}
	if sess.Name == "" {
		// A stored row with no name was registered from the laptop or written
		// by a hook, not created here, so it is named like a discovered one.
		sess.Name = terminalLabel(*sess, others)
	}
}

func (s *Server) handleList(ctx context.Context, req Request) Response {
	var params struct {
		Group string `json:"group"`
	}
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &params)
	}
	list, err := s.loadSessions(ctx)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	out := make([]SessionInfo, 0, len(list))
	for _, sess := range list {
		if params.Group != "" && sess.Group != params.Group {
			continue
		}
		info := sessionInfo(sess, req.Caller)
		s.decorateState(ctx, &info, sess)
		out = append(out, info)
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type":     "session_list",
		"sessions": out,
	}}
}

func (s *Server) handleGet(ctx context.Context, req Request) Response {
	var params struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.ID) == "" {
		return errResp(req.ID, CodeInvalidParams, "id is required")
	}
	list, err := s.loadSessions(ctx)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	sess, ok := resolveSession(list, params.ID)
	if !ok {
		return errResp(req.ID, CodeNotFound, "session not found")
	}
	info := sessionInfo(sess, req.Caller)
	s.decorateState(ctx, &info, sess)
	info.Limits = s.sessionLimits(sess)
	return Response{ID: req.ID, Result: map[string]any{
		"type":    "session",
		"session": info,
	}}
}

// sessionTarget reads a request's target: id, else session. Clients may send both.
func sessionTarget(raw json.RawMessage) string {
	var params struct {
		ID      string `json:"id"`
		Session string `json:"session"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ""
	}
	if id := strings.TrimSpace(params.ID); id != "" {
		return id
	}
	return strings.TrimSpace(params.Session)
}

func (s *Server) resolveReq(ctx context.Context, req Request) (domain.Session, Response, bool) {
	target := sessionTarget(req.Params)
	if target == "" {
		return domain.Session{}, errResp(req.ID, CodeInvalidParams, "id is required"), false
	}
	list, err := s.loadSessions(ctx)
	if err != nil {
		return domain.Session{}, errResp(req.ID, CodeInvalidParams, err.Error()), false
	}
	sess, ok := resolveSession(list, target)
	if !ok {
		return domain.Session{}, errResp(req.ID, CodeNotFound, "session not found"), false
	}
	return sess, Response{}, true
}

// resolveScopedReq is resolveReq for a method that acts on the session: a session
// caller must have it in scope.
func (s *Server) resolveScopedReq(ctx context.Context, req Request) (domain.Session, Response, bool) {
	sess, fail, ok := s.resolveReq(ctx, req)
	if !ok {
		return domain.Session{}, fail, false
	}
	if refused, out := s.refuseOutOfScope(ctx, req, sess.ID); out {
		return domain.Session{}, refused, false
	}
	return sess, Response{}, true
}

func (s *Server) handleRead(ctx context.Context, req Request) Response {
	sess, fail, ok := s.resolveReq(ctx, req)
	if !ok {
		return fail
	}
	var params struct {
		Lines int `json:"lines"`
	}
	_ = json.Unmarshal(req.Params, &params)
	if sess.IsLegacyTmux() {
		return legacyTerminalResp(req.ID, sess)
	}
	var text string
	switch {
	case s.pty != nil:
		screen, cerr := s.captureScreen(sess.ID)
		if cerr != nil {
			return errResp(req.ID, CodeInvalidParams, cerr.Error())
		}
		text = screen
	case s.remote != nil:
		raw, err := usecase.CapturePTYPane(ctx, s.remote, sess.ID, params.Lines)
		if err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
		text = raw
	}
	if params.Lines > 0 {
		text = tailLines(text, params.Lines)
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type":    "pane_read",
		"session": sessionInfo(sess, req.Caller),
		"text":    text,
	}}
}

// legacyTerminalResp refuses a session that still runs in tmux. The message
// names the host and the command that stops it there.
func legacyTerminalResp(id string, sess domain.Session) Response {
	return errResp(id, CodeLegacyTerminal, domain.NewLegacyTmuxError(sess).Error())
}

// promptParams are session.prompt's parameters.
type promptParams struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	Wait    bool   `json:"wait"`
	Until   string `json:"until"`
	Timeout *int   `json:"timeout_ms"`
	Force   bool   `json:"force"`
}

func (s *Server) handlePrompt(ctx context.Context, req Request) Response {
	sess, fail, ok := s.resolveReq(ctx, req)
	if !ok {
		return fail
	}
	var params promptParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	if scope, isSession := s.sessionScopeOf(ctx, req); isSession && !scope.covers(sess.ID) {
		return s.handlePeerPrompt(ctx, req, scope, sess, params)
	}
	if sess.IsLegacyTmux() {
		return legacyTerminalResp(req.ID, sess)
	}
	submitted, refused := s.submitPrompt(ctx, req, sess, params)
	if refused != nil {
		return *refused
	}
	result := map[string]any{"type": "prompt_result", "sent": true}
	if params.Wait {
		until := parseUntil(params.Until)
		if until == "" {
			until = "settled"
		}
		// The wait runs outside the session lock: it gives its admission slot
		// back between ticks, and a prompt holding a slot while it queues on
		// the lock must not keep the waiter from taking one again.
		st, err := s.waitState(ctx, sess, until, params.Timeout, submitted)
		if err != nil {
			return errResp(req.ID, CodeTimeout, err.Error())
		}
		result["state"] = string(st)
	}
	return Response{ID: req.ID, Result: result}
}

// submitPrompt types a prompt into the session under its lock and returns
// when the typing finished, so a wait after it can tell a state sampled
// before the prompt from one sampled after.
func (s *Server) submitPrompt(ctx context.Context, req Request, sess domain.Session, params promptParams) (time.Time, *Response) {
	unlock := s.lockSession(sess.ID)
	defer unlock()
	st, _ := s.classify(ctx, sess)
	if st == domain.AgentStateWaitingInput && !params.Force {
		r := errResp(req.ID, CodeAgentBlocked, "session is waiting for input")
		return time.Time{}, &r
	}
	if s.remote == nil {
		r := errResp(req.ID, CodeInvalidParams, "no remote executor")
		return time.Time{}, &r
	}
	if err := usecase.SendSessionPrompt(ctx, s.remote, sess, params.Text); err != nil {
		code := CodeInvalidParams
		if errors.Is(err, domain.ErrLegacyTmuxSession) {
			code = CodeLegacyTerminal
		}
		r := errResp(req.ID, code, err.Error())
		return time.Time{}, &r
	}
	submitted := time.Now()
	actor := domain.EventActorUser
	if req.Caller != "" && req.Caller == sess.ID {
		actor = domain.EventActorAgent
	}
	s.appendEvent(ctx, sess.ID, actor, domain.EventPromptSubmitted, map[string]any{
		"chars": len(params.Text), "force": params.Force, "wait": params.Wait,
	})
	return submitted, nil
}

// handlePeerPrompt queues session.prompt from outside the target's tree as a
// peer note. Nothing is typed, so force and the waiting_input check do not
// apply and the target is never locked.
func (s *Server) handlePeerPrompt(ctx context.Context, req Request, scope sessionScope, sess domain.Session, params promptParams) Response {
	from, payload, refused := s.admitPeer(ctx, req, scope, sess, params.Text)
	if refused != nil {
		return *refused
	}
	id, refused := s.queuePeerNote(ctx, req, from, sess, payload)
	if refused != nil {
		return *refused
	}
	result := map[string]any{
		"type": "prompt_result", "sent": false, "queued": true,
		"delivered_as": "note", "steer": s.steer.Status(sess.ID),
	}
	if params.Wait {
		until := parseUntil(params.Until)
		if until == "" {
			until = "settled"
		}
		st, err := s.waitPeerNote(ctx, sess, id, until, params.Timeout)
		if err != nil {
			return errResp(req.ID, CodeTimeout, err.Error())
		}
		result["state"] = string(st)
	}
	return Response{ID: req.ID, Result: result}
}

// errPeerNotDelivered is a peer prompt's wait that ran out before the note
// was typed.
var errPeerNotDelivered = errors.New("peer note not delivered yet")

// errPeerNotSettled is a peer prompt's wait that ran out after the note was
// typed but before the target showed it had picked it up.
var errPeerNotSettled = errors.New("peer note delivered, target not settled")

// peerWaitTick is how often a peer prompt's wait polls delivery and the hook.
const peerWaitTick = 50 * time.Millisecond

// waitPeerNote waits, under one timeout, for the note to be typed, then for a
// hook report newer than that submit, so a stale idle is not read as handled,
// then for the target to reach until.
func (s *Server) waitPeerNote(ctx context.Context, sess domain.Session, id int64, until domain.AgentState, timeoutMS *int) (domain.AgentState, error) {
	ms := 120000
	if timeoutMS != nil {
		ms = *timeoutMS
	}
	waitCtx, cancel := ctx, context.CancelFunc(func() {})
	if ms > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, time.Duration(ms)*time.Millisecond)
	}
	defer cancel()
	at, err := pollUntil(waitCtx, func() (time.Time, bool) { return s.Steerer().Delivered(id) })
	if err != nil {
		return "", errPeerNotDelivered
	}
	cur, err := pollUntil(waitCtx, func() (domain.Session, bool) {
		cur, ok := s.findSession(waitCtx, sess.ID)
		return cur, ok && peerNoteSeen(cur, at, time.Now())
	})
	if err != nil {
		return "", errPeerNotSettled
	}
	rest := 0
	if deadline, ok := waitCtx.Deadline(); ok {
		rest = max(1, int(time.Until(deadline).Milliseconds()))
	}
	return s.waitState(waitCtx, cur, until, &rest, at)
}

// peerWaitQuiet is how long after the submit a target without hook reports
// is left before its screen is read. A variable so tests can shorten it.
var peerWaitQuiet = steerClassifierQuiet

// peerNoteSeen reports whether the target can have picked up a note typed at
// at: a hook report newer than the submit, or, for a target that never sent
// one, the classifier's quiet window passed, so the empty composer left
// before the agent redrew is not read as handled.
func peerNoteSeen(sess domain.Session, at, now time.Time) bool {
	if sess.HookStateAt.IsZero() {
		return now.Sub(at) > peerWaitQuiet
	}
	return sess.HookStateAt.After(at)
}

// pollUntil calls check every peerWaitTick until it reports done or ctx ends.
func pollUntil[T any](ctx context.Context, check func() (T, bool)) (T, error) {
	t := time.NewTicker(peerWaitTick)
	defer t.Stop()
	sl := slotFrom(ctx)
	for {
		if v, ok := check(); ok {
			return v, nil
		}
		sl.pause()
		select {
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		case <-t.C:
		}
		sl.resume(ctx)
	}
}

func (s *Server) handleWait(ctx context.Context, req Request) Response {
	sess, fail, ok := s.resolveReq(ctx, req)
	if !ok {
		return fail
	}
	var params struct {
		Until     string `json:"until"`
		TimeoutMS *int   `json:"timeout_ms"`
	}
	_ = json.Unmarshal(req.Params, &params)
	until := parseUntil(params.Until)
	if until == "" {
		until = "settled"
	}
	st, err := s.waitState(ctx, sess, until, params.TimeoutMS, time.Time{})
	if err != nil {
		return errResp(req.ID, CodeTimeout, err.Error())
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type":  "wait_result",
		"state": string(st),
	}}
}

func settled(st domain.AgentState) bool {
	return st == domain.AgentStateIdle || st == domain.AgentStateWaitingInput || st == domain.AgentStateErrored
}

// waitState polls a session until it reaches until or the timeout ends. A
// cached state sampled before since is ignored, so a wait after a prompt does
// not read the state the session had before it.
func (s *Server) waitState(ctx context.Context, sess domain.Session, until domain.AgentState, timeoutMS *int, since time.Time) (domain.AgentState, error) {
	waitCtx := ctx
	cancel := func() {}
	ms := int(DefaultWaitTimeout / time.Millisecond)
	if timeoutMS != nil {
		ms = *timeoutMS
	}
	if ms > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, time.Duration(ms)*time.Millisecond)
	}
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	sl := slotFrom(ctx)
	for {
		st := s.waitSample(waitCtx, sess, since)
		if until == "settled" && settled(st) {
			return st, nil
		}
		if until != "settled" && st == until {
			return st, nil
		}
		sl.pause()
		select {
		case <-waitCtx.Done():
			return st, waitCtx.Err()
		case <-ticker.C:
		}
		sl.resume(waitCtx)
	}
}

// waitSample is a waiter's reading of a session: the state the sweep or the
// last hook report cached, else a classify of its own while nothing has been
// sampled since since, so waiters do not add captures to the sweep's.
func (s *Server) waitSample(ctx context.Context, sess domain.Session, since time.Time) domain.AgentState {
	if c, ok := s.cachedSessionState(sess.ID); ok && c.State != "" && !c.At.Before(since) {
		return domain.AgentState(c.State)
	}
	st, _ := s.classify(ctx, sess)
	return st
}

// createParams are session.create's parameters.
type createParams struct {
	Name   string `json:"name"`
	Group  string `json:"group"`
	Parent string `json:"parent"`
	Orphan bool   `json:"orphan"`
	Quick  bool   `json:"quick"`
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	Agent  string `json:"agent"`
	// Tier is the model tier to launch from: "smart" or "worker". It is
	// exclusive with Agent. Empty means smart for a session the user starts
	// and worker for one created inside a session.
	Tier string `json:"tier"`
	// Model and Effort override the launch for an explicit Agent only.
	Model  string `json:"model"`
	Effort string `json:"effort"`
	Dir    string `json:"dir"`
	Prompt string `json:"prompt"`
	Issue  string `json:"issue"`
	Base   string `json:"base"`
	// ExistingBranch starts from a branch that already exists on the remote
	// instead of creating one.
	ExistingBranch bool `json:"existing_branch"`
	// AgentName is the agent's display name when it differs from the command
	// ("Claude Code" against "claude"). Absent means the command is the name.
	AgentName string `json:"agent_name"`
	// BudgetTokens is the per-session token budget; 0 means none.
	BudgetTokens int64 `json:"budget_tokens"`
	// PromptFree suppresses the task file the skill engine writes from the
	// JIRA issue. A pointer because absent has to keep meaning "true": that is
	// what an agent creating an ad-hoc sibling wants, and it was the only
	// behaviour before callers could ask for a task-driven session.
	PromptFree *bool `json:"prompt_free"`
	// For is a to-do id in the parent's list. The new session is the child
	// that works on it, and the item's Ref links to it.
	For string `json:"for"`
	// Brief is the child's brief (## Question, ## Scope, ## Report must
	// contain). BriefFile is a path on this host to read it from instead.
	Brief     string `json:"brief"`
	BriefFile string `json:"brief_file"`
	// Consult makes the child for item For a hidden smart-tier consult that
	// reviews the parent's design read-only. It needs a brief and is allowed
	// while the parent's design gate is open.
	Consult bool `json:"consult"`
	// Review is "owner/repo#n": the new session reviews that pull request
	// in a worktree at its head.
	Review string `json:"review"`
	// MemoryHigh and MemoryMax override the session's memory limits
	// ("12G", "infinity"). Operator only.
	limitParams
}

func (s *Server) handleCreate(ctx context.Context, req Request) Response {
	var params createParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	if params.Branch != "" {
		if err := domain.ValidBranchName(params.Branch); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	existing, err := s.loadSessions(ctx)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	parentID, perr := domain.ResolveCreateParent(params.Orphan, params.Parent, req.Caller, "", existing)
	if perr != nil {
		return errResp(req.ID, CodeInvalidParams, perr.Error())
	}
	if strings.TrimSpace(params.Review) != "" {
		return s.handleReview(ctx, req, params, parentID)
	}
	if params.Consult {
		return s.handleConsult(ctx, req, params, parentID, existing)
	}
	var forParent *domain.Session
	if strings.TrimSpace(params.For) != "" {
		parent, code, ferr := s.applyFor(&params, parentID, existing)
		if ferr != nil {
			return errResp(req.ID, code, ferr.Error())
		}
		forParent = &parent
	}
	if !params.Quick && (params.Repo == "" || (params.Branch == "" && (params.Name == "" || params.ExistingBranch))) {
		return errResp(req.ID, CodeInvalidParams, "repo and a name or branch are required (or --quick)")
	}
	brief, berr := readBrief(params.Brief, params.BriefFile, false)
	if berr != nil {
		return errResp(req.ID, CodeInvalidParams, berr.Error())
	}
	// A user-started session defaults to smart, a child to worker.
	launch, code, lerr := s.launchFor(ctx, params, defaultTier(parentID))
	if lerr != nil {
		return errResp(req.ID, code, lerr.Error())
	}
	params.Agent, params.AgentName = launch.agent, launch.agentName
	launchModel, launchEffort := launch.model, launch.effort
	sessionTier, requestedTier, tierFallback := launch.tier, launch.requested, launch.fellBack
	name := params.Name
	if name == "" {
		var aerr error
		name, aerr = domain.AssignSessionName(existing, params.Branch, params.Quick)
		if aerr != nil {
			return errResp(req.ID, CodeInvalidParams, aerr.Error())
		}
	} else if err := domain.ValidateSessionName(name); err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	} else if domain.NameTaken(existing, name) {
		return errResp(req.ID, CodeNameTaken, "name already in use")
	}
	branchDerived := !params.Quick && params.Branch == ""
	if branchDerived {
		params.Branch = branchForName(name)
	}
	group := domain.AssignSessionGroup(params.Group, params.Issue, params.Repo, params.Quick)
	promptFree := true
	if params.PromptFree != nil {
		promptFree = *params.PromptFree
	}
	agentName := params.AgentName
	if strings.TrimSpace(agentName) == "" {
		agentName = params.Agent
	}
	if s.create == nil {
		return errResp(req.ID, CodeCreateFailed, "create not configured")
	}
	cfg := domain.SessionConfig{
		Name:          name,
		Group:         group,
		ParentID:      parentID,
		Quick:         params.Quick,
		AdHoc:         params.Quick,
		PromptFree:    promptFree,
		IssueKey:      params.Issue,
		Branch:        params.Branch,
		Directory:     params.Dir,
		InitialPrompt: params.Prompt,
		Brief:         brief,
		BaseBranch:    params.Base,
		// The agent's launch model and effort come from this host's own agent
		// defaults, which serve install keeps in step with the laptop's config,
		// unless the picked tier entry (or an explicit --model) names its own.
		Agent:          &domain.Agent{Name: agentName, Command: params.Agent, LaunchModel: launchModel, LaunchEffort: launchEffort},
		Tier:           sessionTier,
		BudgetTokens:   params.BudgetTokens,
		Repo:           domain.Repo{Name: params.Repo},
		ExistingBranch: params.ExistingBranch,
	}
	var caller *domain.Session
	if found, ok := resolveSession(existing, req.Caller); ok {
		caller = &found
	}
	if forParent != nil {
		// The child is a sibling worktree of its parent's repository.
		caller = forParent
	}
	if root := createGitRoot(caller, existing, hostHome()); root != "" {
		cfg.SSHManager = local.NewExecutor(root)
	}
	if !params.empty() {
		// The override is stored under the id before the terminal starts.
		cfg.SessionID = uuid.NewString()
		if refused := s.createLimits(ctx, req, params.limitParams, cfg.SessionID); refused != nil {
			return *refused
		}
	}
	sess, err := s.createOnFreeBranch(ctx, cfg, branchDerived)
	if err != nil {
		if cfg.SessionID != "" {
			s.resourceOptions().Overrides.Remove(cfg.SessionID)
		}
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	params.Branch = sess.Branch
	sess.Name = name
	sess.Group = group
	sess.ParentID = parentID
	// The creator copies these from the config it was given; keep the row
	// honest even for a creator that does not.
	sess.Tier = sessionTier
	if sess.LaunchModel == "" {
		sess.LaunchModel = launchModel
	}
	if sess.LaunchEffort == "" {
		sess.LaunchEffort = launchEffort
	}
	if s.repo != nil {
		if err := s.repo.Save(ctx, sess); err != nil {
			return errResp(req.ID, CodeCreateFailed, err.Error())
		}
	}
	if forParent != nil {
		if err := s.linkChild(ctx, forParent.ID, params.For, sess.ID); err != nil {
			// An unlinked child is never tracked, and its row, worktree and branch
			// would block a retry, so it is torn down.
			if derr := s.discardChild(ctx, *sess); derr != nil {
				log.Printf("session.create: discard unlinked child %s: %v", sess.ID, derr)
			}
			return errResp(req.ID, CodeCreateFailed, err.Error())
		}
	}
	s.appendEvent(ctx, sess.ID, domain.EventActorAiman, domain.EventSessionCreated, map[string]any{
		"name": name, "group": group, "repo": params.Repo, "branch": params.Branch,
		"agent": params.Agent, "quick": params.Quick,
		"tier": sessionTier, "requested_tier": requestedTier,
		"model": launchModel, "effort": launchEffort, "tier_fallback": tierFallback,
	})
	info := sessionInfo(*sess, req.Caller)
	return Response{ID: req.ID, Result: map[string]any{
		"type": "session", "session": info,
		"tier_fallback": tierFallback, "requested_tier": requestedTier,
	}}
}

// worktreeExistsError is the text git.Manager.SetupRemoteWorktree fails with when
// the branch's worktree directory is already there.
const worktreeExistsError = "WORKTREE_EXISTS"

// maxBranchSuffix bounds the search for a free worktree under a derived branch.
const maxBranchSuffix = 20

// branchForName derives a branch from a session name. A name with nothing usable
// in a branch, such as one in another alphabet, or a derived name git would
// refuse gets a generated identifier.
func branchForName(name string) string {
	if branch := domain.BranchFromSessionName(name); domain.ValidBranchName(branch) == nil {
		return branch
	}
	return domain.AdHocLabel("session-", time.Now(), rand.Reader)
}

// createOnFreeBranch runs the create. A derived branch whose worktree already
// exists takes the next suffix, so a new session never lands in the files of an
// old one. A branch the caller named is that branch, and its collision is
// reported.
func (s *Server) createOnFreeBranch(ctx context.Context, cfg domain.SessionConfig, derived bool) (*domain.Session, error) {
	sess, err := s.create.CreateSession(ctx, cfg)
	base := cfg.Branch
	for n := 2; derived && n <= maxBranchSuffix && err != nil && strings.Contains(err.Error(), worktreeExistsError); n++ {
		cfg.Branch = fmt.Sprintf("%s-%d", base, n)
		sess, err = s.create.CreateSession(ctx, cfg)
	}
	if err != nil {
		return nil, err
	}
	if sess.Branch == "" {
		sess.Branch = cfg.Branch
	}
	return sess, nil
}

// tierChoice validates the tier, model and effort parameters. A tier is
// exclusive with an explicit agent, and a model or effort needs an agent to
// apply to.
func (s *Server) tierChoice(params createParams) (tier, model, effort string, err error) {
	tier = strings.ToLower(strings.TrimSpace(params.Tier))
	model = strings.TrimSpace(params.Model)
	effort = strings.TrimSpace(params.Effort)
	agent := strings.TrimSpace(params.Agent)
	if tier != "" && !config.ValidTier(tier) {
		return "", "", "", fmt.Errorf("tier must be %s or %s, not %q", config.TierSmart, config.TierWorker, params.Tier)
	}
	if tier != "" && agent != "" {
		return "", "", "", errors.New("tier and agent are exclusive: a tier picks the agent")
	}
	if (model != "" || effort != "") && agent == "" {
		return "", "", "", errors.New("model and effort need an explicit agent")
	}
	return tier, model, effort, nil
}

// launchPick is the agent a create launches and where it came from: an
// explicit agent, or the first installed entry of a tier.
type launchPick struct {
	agent, agentName, model, effort string
	// tier is the tier the agent was picked from after fallback, requested
	// the one asked for; both are empty for an explicit agent.
	tier, requested string
	fellBack        bool
}

// launchFor validates the tier, model and effort parameters and picks the
// agent. An explicit agent always wins; only an empty one reads the tiers,
// starting from the requested tier or else fallback. The returned code is
// the error's response code.
func (s *Server) launchFor(ctx context.Context, params createParams, fallback string) (launchPick, string, error) {
	tier, model, effort, err := s.tierChoice(params)
	if err != nil {
		return launchPick{}, CodeInvalidParams, err
	}
	if strings.TrimSpace(params.Agent) != "" {
		return launchPick{agent: params.Agent, agentName: params.AgentName, model: model, effort: effort}, "", nil
	}
	if tier == "" {
		tier = fallback
	}
	picked, err := s.pickAgent(ctx, tier)
	if err != nil {
		return launchPick{}, CodeNoPreferredAgent, err
	}
	return launchPick{
		agent: picked.Entry.Agent, model: picked.Entry.Model, effort: picked.Entry.Effort,
		tier: picked.Tier, requested: picked.Requested, fellBack: picked.FellBack,
	}, "", nil
}

// defaultTier is the tier a create starts from when none was asked for: the
// smart tier for a session the user starts, the worker tier for a child
// created inside a session.
func defaultTier(parentID string) string {
	if strings.TrimSpace(parentID) == "" {
		return config.TierSmart
	}
	return config.TierWorker
}

// applyFor fills a child's create parameters from its parent and the parent's
// to-do item params.For. The item must exist and carry no Ref. The returned
// code is the error's response code.
func (s *Server) applyFor(params *createParams, parentID string, existing []domain.Session) (domain.Session, string, error) {
	params.For = strings.TrimSpace(params.For)
	if parentID == "" {
		return domain.Session{}, CodeInvalidParams, errors.New("for needs a parent session (--parent, or run inside one)")
	}
	parent, ok := resolveSession(existing, parentID)
	if !ok {
		return domain.Session{}, CodeNotFound, fmt.Errorf("parent session %s not found", parentID)
	}
	if parent.Branch == "" {
		return domain.Session{}, CodeInvalidParams, fmt.Errorf("parent session %s has no branch to cut a child from", parent.Name)
	}
	list, err := s.TodoAccess().Get(parent.ID)
	if err != nil {
		return domain.Session{}, CodeInvalidParams, err
	}
	i, err := findTodo(&list, params.For)
	if err != nil {
		return domain.Session{}, CodeNotFound, fmt.Errorf("%w in session %s", err, parent.Name)
	}
	if todo.GateOpen(list, todo.DesignGateRef) {
		return domain.Session{}, CodeDesignNotApproved, fmt.Errorf("session %s has an open design gate: get the operator's approval, then run aiman todo approve-design", parent.Name)
	}
	item := list.Items[i]
	if item.Ref != "" {
		return domain.Session{}, CodeInvalidParams, fmt.Errorf("to-do %s is already linked to %s", item.ID, item.Ref)
	}
	if params.Branch == "" {
		params.Branch = coordinator.ChildBranch(parent.Branch, item.ID)
	}
	// The parent's branch exists only in the parent's repository; a child in
	// another one starts from that repository's default branch.
	repo := strings.TrimSpace(params.Repo)
	if repo == "" || strings.EqualFold(repo, parent.RepoName) {
		if strings.TrimSpace(params.Base) == "" {
			params.Base = parent.Branch
		}
	}
	defaults := []struct {
		dst *string
		src string
	}{
		{&params.Repo, parent.RepoName},
		{&params.Issue, parent.IssueKey},
		{&params.Group, parent.Group},
		{&params.Prompt, item.Text},
	}
	for _, d := range defaults {
		if strings.TrimSpace(*d.dst) == "" {
			*d.dst = d.src
		}
	}
	return parent, "", nil
}

// readBrief validates the brief text or the file it is in, and returns it in
// its canonical form: a consult brief when consult is set, else a child brief
// followed by coordinator.ChildGuidelines. Neither given is no brief.
func readBrief(text, file string, consult bool) (string, error) {
	if text == "" && file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("brief_file: %w", err)
		}
		text = string(raw)
	}
	if strings.TrimSpace(text) == "" {
		return "", nil
	}
	if consult {
		b, err := coordinator.ParseConsultBrief(text)
		if err != nil {
			return "", err
		}
		return b.Markdown(), nil
	}
	b, err := coordinator.ParseBrief(text)
	if err != nil {
		return "", err
	}
	return b.Markdown() + "\n" + coordinator.ChildGuidelines + "\n", nil
}

// linkChild sets the parent's to-do Ref to the child as aiman. It refuses an
// item linked meanwhile, so two creates for one item cannot both link.
func (s *Server) linkChild(ctx context.Context, parentID, todoID, childID string) error {
	return s.TodoAccess().Mutate(ctx, parentID, domain.AuthorAiman, func(l *domain.TodoList) error {
		i, err := findTodo(l, todoID)
		if err != nil {
			return fmt.Errorf("link child: %w", err)
		}
		if ref := l.Items[i].Ref; ref != "" {
			return fmt.Errorf("to-do %s was linked to %s while the child was created", todoID, ref)
		}
		l.Items[i].Ref = coordinator.ChildRef(childID)
		return nil
	})
}

func (s *Server) handleRename(ctx context.Context, req Request) Response {
	sess, fail, ok := s.resolveReq(ctx, req)
	if !ok {
		return fail
	}
	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
		return errResp(req.ID, CodeInvalidParams, "name is required")
	}
	if err := domain.ValidateSessionName(params.Name); err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	list, err := s.loadSessions(ctx)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	others := make([]domain.Session, 0, len(list))
	for _, e := range list {
		if e.ID != sess.ID {
			others = append(others, e)
		}
	}
	if domain.NameTaken(others, params.Name) {
		return errResp(req.ID, CodeNameTaken, "name already in use")
	}
	sess.Name = params.Name
	if s.repo != nil {
		if err := s.repo.Save(ctx, &sess); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	info := sessionInfo(sess, req.Caller)
	return Response{ID: req.ID, Result: map[string]any{"type": "session", "session": info}}
}

// handleForget removes a session from this host's database and drops whatever
// the PTY runtime still holds for it.
//
// Teardown runs from the dashboard, which deletes its own row — but serve keeps
// its own database, and the dashboard rebuilds its list from serve's live
// stream. Without this the row came straight back on the next poll, so a
// terminated session was unkillable from the UI: ctrl+k appeared to do nothing.
//
// Forgetting is deliberately independent of whether the terminal is still
// alive. The caller has already torn it down, and refusing to forget a session
// whose holder outlived the kill would put the row right back out of reach.
// The handover is written first. A failed write is logged and the row is
// still deleted.
// discardChild undoes a create that cannot stand: it stops the terminal on
// either backend, removes the worktree and branch the create made, then
// drops the row. Each step is best effort so a partial failure still frees
// the name for a retry.
func (s *Server) discardChild(ctx context.Context, sess domain.Session) error {
	if err := (serverTerminal{s}).Stop(ctx, sess); err != nil {
		log.Printf("session.create: stop %s: %v", sess.ID, err)
	}
	if s.remote != nil && sess.WorktreePath != "" {
		if _, err := s.remote.Execute(ctx, discardWorktreeScript(sess.WorktreePath, sess.Branch)); err != nil {
			log.Printf("session.create: remove worktree of %s: %v", sess.ID, err)
		}
	}
	return s.dropSession(ctx, sess)
}

// discardWorktreeScript removes a linked worktree and its branch from the main
// checkout. git refuses to remove a worktree from inside it, so the main
// checkout is found through the common git dir. A path that is not a linked
// worktree is left alone.
func discardWorktreeScript(worktree, branch string) string {
	script := "wt=" + quoteArg(worktree) + `
main="$(cd "$wt" 2>/dev/null && cd "$(git rev-parse --git-common-dir)/.." && pwd)" || exit 0
[ -n "$main" ] && [ "$main" != "$(cd "$wt" && pwd)" ] || exit 0
git -C "$main" worktree remove --force "$wt"
`
	if branch != "" {
		script += `git -C "$main" branch -D ` + quoteArg(branch) + " >/dev/null 2>&1 || true\n"
	}
	return script
}

// dropSession stops a session's pty and deletes everything serve holds for
// it: steering queue, notification state, CI rows, the stored row, and a
// review session's sandbox and scratch.
func (s *Server) dropSession(ctx context.Context, sess domain.Session) error {
	s.downReviewSandbox(ctx, sess)
	if sess.ReviewPR != "" {
		s.dropReviewFixers(ctx, sess.ID)
		s.removeReviewFiles(sess.ID)
	}
	if s.pty != nil {
		s.stopDebugPTY(sess.ID)
		_ = s.pty.Kill(sess.ID)
		_ = s.pty.Forget(sess.ID)
	}
	s.forgetResources(sess.ID)
	s.steer.Forget(sess.ID)
	s.forgetCILocal(sess.ID)
	s.uncertainPanes.Delete(sess.ID)
	s.sessionStates.Delete(sess.ID)
	if rec := s.NotifyRecorder(); rec != nil {
		rec.Forget(sess.ID)
	}
	if s.ciWatch != nil {
		// A failed reset leaves orphan CI rows; it must not block the delete.
		if err := s.ciWatch.Forget(ctx, sess.ID); err != nil {
			log.Printf("ci: forget %s: %v", sess.ID, err)
		}
	}
	if s.repo != nil {
		return s.repo.Delete(ctx, sess.ID)
	}
	return nil
}

func (s *Server) handleForget(ctx context.Context, req Request) Response {
	sess, fail, ok := s.resolveScopedReq(ctx, req)
	if !ok {
		return fail
	}
	s.noteForgetHandover(ctx, sess)
	if err := s.dropSession(ctx, sess); err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	s.appendEvent(ctx, sess.ID, domain.EventActorUser, domain.EventSessionEnded, map[string]any{"reason": "forget"})
	return Response{ID: req.ID, Result: map[string]any{
		"type": "session_forgotten",
		"id":   sess.ID,
		"name": sess.Name,
	}}
}

func (s *Server) handleMove(ctx context.Context, req Request) Response {
	sess, fail, ok := s.resolveReq(ctx, req)
	if !ok {
		return fail
	}
	var params struct {
		Group string `json:"group"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Group == "" {
		return errResp(req.ID, CodeInvalidParams, "group is required")
	}
	if err := domain.ValidateGroupName(params.Group); err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	sess.Group = params.Group
	if s.repo != nil {
		if err := s.repo.Save(ctx, &sess); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	info := sessionInfo(sess, req.Caller)
	return Response{ID: req.ID, Result: map[string]any{"type": "session", "session": info}}
}

func (s *Server) handleReportAgentSession(ctx context.Context, req Request) Response {
	var params struct {
		ID               string `json:"id"`
		AgentSessionID   string `json:"agent_session_id"`
		AgentSessionPath string `json:"agent_session_path"`
		State            string `json:"state"`
		Source           string `json:"source"`
		Message          string `json:"message"`
		Title            string `json:"title"`
		Ended            bool   `json:"ended"`
		Seq              int64  `json:"seq"`
		Event            string `json:"event"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return errResp(req.ID, CodeInvalidParams, "invalid params")
		}
	}
	sessionID := strings.TrimSpace(params.ID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(req.Caller)
	}
	rep := agenthook.Report{
		Native: agenthook.Native{
			ID:   strings.TrimSpace(params.AgentSessionID),
			Path: strings.TrimSpace(params.AgentSessionPath),
		},
		State:   domain.AgentState(strings.TrimSpace(params.State)),
		Source:  strings.TrimSpace(params.Source),
		Message: strings.TrimSpace(params.Message),
		Title:   strings.TrimSpace(params.Title),
		Ended:   params.Ended,
		Seq:     params.Seq,
		Event:   strings.TrimSpace(params.Event),
	}
	if sessionID == "" || (rep.ID == "" && rep.State == "" && !rep.Ended && rep.Title == "") {
		return errResp(req.ID, CodeInvalidParams, "id plus agent_session_id, state, title, or ended is required")
	}
	if s.repo != nil {
		if err := s.applyAgentReport(ctx, sessionID, rep); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type":               "agent_session",
		"id":                 sessionID,
		"agent_session_id":   rep.ID,
		"agent_session_path": rep.Path,
		"state":              string(rep.State),
		"title":              rep.Title,
		"ended":              rep.Ended,
	}}
}

// applyAgentReport folds a hook report into the session it names.
//
// A session with no row that is running here is stored from its live record,
// except a hidden helper (reviewer, handover, consult): its agent reports a
// few seconds before the creator saves the row, and a row saved from the live
// record first would be named after its terminal. That report reaches the
// recorder, the steerer and the ledger but is not saved; the creator's save
// is the first identity write. The helper's row therefore has no agent
// session id until its second report.
func (s *Server) applyAgentReport(ctx context.Context, sessionID string, rep agenthook.Report) error {
	sess, err := s.repo.Get(ctx, sessionID)
	persist := true
	if err != nil {
		sess = s.liveSessionByID(ctx, sessionID)
		persist = sess != nil && !sess.Role.Hidden()
		if persist {
			s.backfillNew(ctx, sess)
		}
	}
	if sess == nil {
		return nil
	}
	wasEnded := sess.AgentEnded
	agenthook.ApplyReport(sess, rep, time.Now())
	if persist {
		if err := s.repo.Save(ctx, sess); err != nil {
			return err
		}
	}
	s.observeState(ctx, *sess)
	if clr, ok := s.repo.(agentEndedClearer); ok && persist && wasEnded && !sess.AgentEnded {
		if err := clr.ClearAgentEnded(ctx, sess.ID); err != nil {
			return err
		}
	}
	// Compaction waits on this hook, so the file is the pre-compact pane.
	// SessionEnd does not write a handover.
	if strings.EqualFold(rep.Event, agenthook.EventPreCompact) {
		s.notePreCompactHandover(ctx, *sess)
	}
	// A new state may open the delivery gate for held notes.
	s.steer.Kick(sess.ID)
	// Kick only asks for a scan. A transcript error stays in that scan.
	s.kickUsage()
	s.appendEvent(ctx, sess.ID, domain.EventActorAgent, domain.EventHookReported, map[string]any{
		"event": rep.Event, "state": string(rep.State), "source": rep.Source,
		"message": rep.Message, "title": rep.Title, "seq": rep.Seq, "ended": rep.Ended,
	})
	if rep.Ended {
		s.appendEvent(ctx, sess.ID, domain.EventActorAgent, domain.EventSessionEnded, map[string]any{
			"reason": "session_end", "event": rep.Event,
		})
	}
	return nil
}

func resolveSession(list []domain.Session, target string) (domain.Session, bool) {
	target = strings.TrimSpace(target)
	lower := strings.ToLower(target)
	for _, s := range list {
		if strings.ToLower(s.Name) == lower {
			return s, true
		}
	}
	if g, name, ok := strings.Cut(target, "/"); ok {
		nl := strings.ToLower(name)
		for _, s := range list {
			if s.Group == g && strings.ToLower(s.Name) == nl {
				return s, true
			}
		}
	}
	for _, s := range list {
		if s.ID == target {
			return s, true
		}
	}
	for _, s := range list {
		if s.TerminalName == target {
			return s, true
		}
	}
	return domain.Session{}, false
}

// agentEndedClearer is implemented by repositories that can retract a stored
// SessionEnd; Save alone cannot, because it never writes an empty value over a
// non-empty one.
type agentEndedClearer interface {
	ClearAgentEnded(ctx context.Context, id string) error
}

// liveSessionByID returns the session with this id when it is running on this
// host but has no row yet, or nil.
func (s *Server) liveSessionByID(ctx context.Context, id string) *domain.Session {
	list, err := s.loadSessions(ctx)
	if err != nil {
		return nil
	}
	for i := range list {
		if list[i].ID == id {
			sess := list[i]
			return &sess
		}
	}
	return nil
}

// registrableSession builds the row serve stores for a session the laptop
// registers. Identity comes from the laptop; state that serve observes first
// hand (hook reports, status) is kept from the existing row, and fields only
// meaningful on the laptop are dropped.
func registrableSession(in domain.Session, existing *domain.Session) domain.Session {
	out := in
	out.LocalPath = ""
	out.MutagenSyncID = ""
	out.Tunnels = nil
	out.AWSConfig = nil
	if existing == nil {
		return out
	}
	out.HookState = existing.HookState
	out.HookStateMessage = existing.HookStateMessage
	out.HookStateSource = existing.HookStateSource
	out.HookStateSeq = existing.HookStateSeq
	out.HookStateAt = existing.HookStateAt
	out.AgentEnded = existing.AgentEnded
	out.AgentTitle = existing.AgentTitle
	out.Status = existing.Status
	return out
}

// dropSwitchedLaunchModel clears the stored launch model and effort when the
// laptop registers a session under a different agent and sends none: they
// belong to the previous agent, and Save keeps them otherwise.
func (s *Server) dropSwitchedLaunchModel(ctx context.Context, row domain.Session, existing *domain.Session) error {
	if existing == nil || row.AgentName == "" || strings.EqualFold(row.AgentName, existing.AgentName) {
		return nil
	}
	if row.LaunchModel != "" || row.LaunchEffort != "" || (existing.LaunchModel == "" && existing.LaunchEffort == "") {
		return nil
	}
	clearer, ok := s.repo.(interface {
		ClearLaunchModel(context.Context, string) error
	})
	if !ok {
		return nil
	}
	return clearer.ClearLaunchModel(ctx, row.ID)
}

func (s *Server) handleRegister(ctx context.Context, req Request) Response {
	// Registering writes identity, parent_id included, so a session could
	// adopt any other session and widen its own scope.
	if s.isSessionCall(ctx, req) {
		return errResp(req.ID, CodeNeedsOperator, "only the operator may register sessions")
	}
	var params struct {
		Sessions []domain.Session `json:"sessions"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return errResp(req.ID, CodeInvalidParams, "invalid params")
		}
	}
	ids := []string{}
	if s.repo != nil {
		for _, in := range params.Sessions {
			id := strings.TrimSpace(in.ID)
			if id == "" || strings.HasPrefix(id, "pending-") {
				continue
			}
			in.ID = id
			existing, err := s.repo.Get(ctx, id)
			if err != nil {
				existing = nil
			}
			row := registrableSession(in, existing)
			if existing != nil {
				// Save keeps a stored name or group for an empty incoming one;
				// carry them so the backfill does not invent a new one.
				if row.Name == "" {
					row.Name = existing.Name
				}
				if row.Group == "" {
					row.Group = existing.Group
				}
			}
			s.backfillNew(ctx, &row)
			if err := s.repo.Save(ctx, &row); err != nil {
				return errResp(req.ID, CodeInvalidParams, err.Error())
			}
			if err := s.dropSwitchedLaunchModel(ctx, row, existing); err != nil {
				return errResp(req.ID, CodeInvalidParams, err.Error())
			}
			if existing == nil {
				s.appendEvent(ctx, id, domain.EventActorAiman, domain.EventSessionCreated, map[string]any{
					"name": row.Name, "group": row.Group, "source": "register",
				})
			}
			ids = append(ids, id)
		}
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "sessions_registered", "ids": ids}}
}
