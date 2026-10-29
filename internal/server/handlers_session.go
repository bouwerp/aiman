package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/coordinator"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/local"
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
	Tier             string `json:"tier,omitempty"`
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
	Self        bool `json:"self"`
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
		AgentSessionID:   s.AgentSessionID,
		AgentSessionPath: s.AgentSessionPath,
		Title:            s.AgentTitle,
		Status:           string(s.Status),
		State:            string(domain.AgentStateUnknown),
		Ended:            s.AgentEnded,
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

func (s *Server) decorateState(ctx context.Context, info *SessionInfo, sess domain.Session) {
	live := s.liveSession(ctx, sess)
	info.Title = live.AgentTitle
	info.Ended = live.AgentEnded
	info.Unprotected = s.verifier != nil && s.verifier.BeforePR() && !agenthook.GuardsPRCreate(live.AgentName)
	st, conf := s.classify(ctx, live)
	info.State = string(st)
	switch conf {
	case pane.High:
		info.StateConfidence = "high"
	default:
		info.StateConfidence = "low"
	}
	if live.HookStateMessage != "" && st == domain.AgentStateWaitingInput {
		info.StateMessage = live.HookStateMessage
	}
	if live.HookStateSource != "" && conf == pane.High {
		info.StateSource = live.HookStateSource
	}
}

func (s *Server) classify(ctx context.Context, sess domain.Session) (domain.AgentState, pane.Confidence) {
	live := s.liveSession(ctx, sess)
	if st, ok := agenthook.ResolveHookState(live, time.Now()); ok {
		return st, pane.High
	}
	if s.pty == nil || live.IsLegacyTmux() {
		return domain.AgentStateUnknown, pane.Low
	}
	// The PTY runtime lives in this process. pane.Classify reads a rendered
	// screen, not the raw spool.
	screen, cerr := s.pty.CaptureScreen(live.ID)
	if cerr != nil {
		return domain.AgentStateUnknown, pane.Low
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
	s.recordUncertainPane(ctx, live, screen, r)
	return r.State, r.Confidence
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
			out[i].Status = l.Status
			continue
		}
		byID[l.ID] = len(out)
		out = append(out, l)
	}
	return out
}

func (s *Server) loadSessions(ctx context.Context) ([]domain.Session, error) {
	if s.repo == nil {
		return nil, nil
	}
	list, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	// Backfill and persist names for rows this host actually owns, before
	// anything discovered is folded in — the re-read below would discard it.
	changed := false
	for i := range list {
		if list[i].Name == "" || list[i].Group == "" {
			s.backfill(&list[i], list)
			if err := s.repo.Save(ctx, &list[i]); err != nil {
				return nil, err
			}
			changed = true
		}
	}
	if changed {
		list, err = s.repo.List(ctx)
		if err != nil {
			return nil, err
		}
	}

	// Then fold in what is running here. Discovered sessions absent from the
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
	return Response{ID: req.ID, Result: map[string]any{
		"type":    "session",
		"session": info,
	}}
}

func (s *Server) resolveReq(ctx context.Context, req Request) (domain.Session, Response, bool) {
	var params struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.ID) == "" {
		return domain.Session{}, errResp(req.ID, CodeInvalidParams, "id is required"), false
	}
	list, err := s.loadSessions(ctx)
	if err != nil {
		return domain.Session{}, errResp(req.ID, CodeInvalidParams, err.Error()), false
	}
	sess, ok := resolveSession(list, params.ID)
	if !ok {
		return domain.Session{}, errResp(req.ID, CodeNotFound, "session not found"), false
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
		screen, cerr := s.pty.CaptureScreen(sess.ID)
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

func (s *Server) handlePrompt(ctx context.Context, req Request) Response {
	sess, fail, ok := s.resolveReq(ctx, req)
	if !ok {
		return fail
	}
	var params struct {
		ID      string `json:"id"`
		Text    string `json:"text"`
		Wait    bool   `json:"wait"`
		Until   string `json:"until"`
		Timeout *int   `json:"timeout_ms"`
		Force   bool   `json:"force"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	if sess.IsLegacyTmux() {
		return legacyTerminalResp(req.ID, sess)
	}
	unlock := s.lockSession(sess.ID)
	defer unlock()
	st, _ := s.classify(ctx, sess)
	if st == domain.AgentStateWaitingInput && !params.Force {
		return errResp(req.ID, CodeAgentBlocked, "session is waiting for input")
	}
	if s.remote == nil {
		return errResp(req.ID, CodeInvalidParams, "no remote executor")
	}
	if err := usecase.SendSessionPrompt(ctx, s.remote, sess, params.Text); err != nil {
		if errors.Is(err, domain.ErrLegacyTmuxSession) {
			return errResp(req.ID, CodeLegacyTerminal, err.Error())
		}
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	actor := domain.EventActorUser
	if req.Caller != "" && req.Caller == sess.ID {
		actor = domain.EventActorAgent
	}
	s.appendEvent(ctx, sess.ID, actor, domain.EventPromptSubmitted, map[string]any{
		"chars": len(params.Text), "force": params.Force, "wait": params.Wait,
	})
	result := map[string]any{"type": "prompt_result", "sent": true}
	if params.Wait {
		until := parseUntil(params.Until)
		if until == "" {
			until = "settled"
		}
		st, err := s.waitState(ctx, sess, until, params.Timeout)
		if err != nil {
			return errResp(req.ID, CodeTimeout, err.Error())
		}
		result["state"] = string(st)
	}
	return Response{ID: req.ID, Result: result}
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
	st, err := s.waitState(ctx, sess, until, params.TimeoutMS)
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

func (s *Server) waitState(ctx context.Context, sess domain.Session, until domain.AgentState, timeoutMS *int) (domain.AgentState, error) {
	waitCtx := ctx
	cancel := func() {}
	ms := 120000
	if timeoutMS != nil {
		ms = *timeoutMS
	}
	if ms > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, time.Duration(ms)*time.Millisecond)
	}
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		st, _ := s.classify(waitCtx, sess)
		if until == "settled" && settled(st) {
			return st, nil
		}
		if until != "settled" && st == until {
			return st, nil
		}
		select {
		case <-waitCtx.Done():
			return st, waitCtx.Err()
		case <-ticker.C:
		}
	}
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
}

func (s *Server) handleCreate(ctx context.Context, req Request) Response {
	var params createParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	existing, err := s.loadSessions(ctx)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	parentID, perr := domain.ResolveCreateParent(params.Orphan, params.Parent, req.Caller, "", existing)
	if perr != nil {
		return errResp(req.ID, CodeInvalidParams, perr.Error())
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
	if !params.Quick && (params.Repo == "" || params.Branch == "") {
		return errResp(req.ID, CodeInvalidParams, "repo and branch are required (or --quick)")
	}
	brief, berr := readBrief(params.Brief, params.BriefFile, false)
	if berr != nil {
		return errResp(req.ID, CodeInvalidParams, berr.Error())
	}
	tier, model, effort, terr := s.tierChoice(params)
	if terr != nil {
		return errResp(req.ID, CodeInvalidParams, terr.Error())
	}
	var launchModel, launchEffort, sessionTier, requestedTier string
	tierFallback := false
	if strings.TrimSpace(params.Agent) == "" {
		// An explicit agent always wins; only an empty one reads the tiers. A
		// user-started session defaults to smart, a child to worker.
		want := tier
		if want == "" {
			want = defaultTier(parentID)
		}
		picked, perr := s.pickAgent(ctx, want)
		if perr != nil {
			return errResp(req.ID, CodeNoPreferredAgent, perr.Error())
		}
		params.Agent, params.AgentName = picked.Entry.Agent, ""
		launchModel, launchEffort = picked.Entry.Model, picked.Entry.Effort
		sessionTier, requestedTier, tierFallback = picked.Tier, picked.Requested, picked.FellBack
	} else {
		launchModel, launchEffort = model, effort
	}
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
	sess, err := s.create.CreateSession(ctx, cfg)
	if err != nil {
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
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
	if todo.GateOpen(list) {
		return domain.Session{}, CodeDesignNotApproved, fmt.Errorf("session %s has an open design gate: get the operator's approval, then run aiman todo approve-design", parent.Name)
	}
	item := list.Items[i]
	if item.Ref != "" {
		return domain.Session{}, CodeInvalidParams, fmt.Errorf("to-do %s is already linked to %s", item.ID, item.Ref)
	}
	if params.Branch == "" {
		params.Branch = coordinator.ChildBranch(parent.Branch, item.ID)
	}
	defaults := []struct {
		dst *string
		src string
	}{
		{&params.Base, parent.Branch},
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
// its canonical form: a consult brief when consult is set, else a child brief.
// Neither given is no brief.
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
	return b.Markdown(), nil
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
// it: steering queue, notification state, CI rows and the stored row.
func (s *Server) dropSession(ctx context.Context, sess domain.Session) error {
	if s.pty != nil {
		_ = s.pty.Kill(sess.ID)
		_ = s.pty.Forget(sess.ID)
	}
	s.steer.Forget(sess.ID)
	s.uncertainPanes.Delete(sess.ID)
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
	sess, fail, ok := s.resolveReq(ctx, req)
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
		sess, err := s.repo.Get(ctx, sessionID)
		if err != nil {
			sess = s.liveSessionByID(ctx, sessionID)
		}
		if sess != nil {
			wasEnded := sess.AgentEnded
			agenthook.ApplyReport(sess, rep, time.Now())
			if err := s.repo.Save(ctx, sess); err != nil {
				return errResp(req.ID, CodeInvalidParams, err.Error())
			}
			s.notifyObserve(ctx, *sess)
			if clr, ok := s.repo.(agentEndedClearer); ok && wasEnded && !sess.AgentEnded {
				if err := clr.ClearAgentEnded(ctx, sess.ID); err != nil {
					return errResp(req.ID, CodeInvalidParams, err.Error())
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
