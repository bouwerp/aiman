package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/local"
	"github.com/bouwerp/aiman/internal/pane"
	"github.com/bouwerp/aiman/internal/usecase"
)

type SessionInfo struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Group            string `json:"group"`
	ParentID         string `json:"parent_id,omitempty"`
	IssueKey         string `json:"issue_key,omitempty"`
	Branch           string `json:"branch,omitempty"`
	RepoName         string `json:"repo_name,omitempty"`
	TmuxSession      string `json:"tmux_session,omitempty"`
	WorktreePath     string `json:"worktree_path,omitempty"`
	WorkingDirectory string `json:"working_directory,omitempty"`
	AgentName        string `json:"agent_name,omitempty"`
	AgentSessionID   string `json:"agent_session_id,omitempty"`
	AgentSessionPath string `json:"agent_session_path,omitempty"`
	Title            string `json:"title,omitempty"`
	Status           string `json:"status,omitempty"`
	State            string `json:"state,omitempty"`
	StateConfidence  string `json:"state_confidence,omitempty"`
	StateMessage     string `json:"state_message,omitempty"`
	StateSource      string `json:"state_source,omitempty"`
	Ended            bool   `json:"ended,omitempty"`
	Self             bool   `json:"self"`
}

func sessionInfo(s domain.Session, callerID string) SessionInfo {
	return SessionInfo{
		ID:               s.ID,
		Name:             s.Name,
		Group:            s.Group,
		ParentID:         s.ParentID,
		IssueKey:         s.IssueKey,
		Branch:           s.Branch,
		RepoName:         s.RepoName,
		TmuxSession:      s.TmuxSession,
		WorktreePath:     s.WorktreePath,
		WorkingDirectory: s.WorkingDirectory,
		AgentName:        s.AgentName,
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
	if s.remote == nil || live.TmuxSession == "" {
		return domain.AgentStateUnknown, pane.Low
	}
	if live.IsPTY() {
		// The PTY runtime lives in this process; skip the remote hop.
		// pane.Classify was written against rendered tmux output, so it needs a
		// screen here too, not the raw spool.
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
		return r.State, r.Confidence
	}
	text, err := s.remote.CaptureTmuxPane(ctx, live.TmuxSession)
	if err != nil {
		return domain.AgentStateUnknown, pane.Low
	}
	r := pane.Classify(pane.Observation{Pane: text})
	return r.State, r.Confidence
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
// were running. serve holds a local executor, so it can just look.
//
// A tmux session is only counted when it carries AIMAN_ID, which is both the
// join key and the filter: it excludes tmux sessions that are not aiman's,
// including serve's and the trigger daemon's own.
func (s *Server) liveSessions(ctx context.Context) []domain.Session {
	if s.remote == nil {
		return nil
	}
	var out []domain.Session

	names, err := s.remote.ScanTmuxSessions(ctx)
	if err == nil {
		for _, name := range names {
			id, iderr := s.remote.GetTmuxSessionEnv(ctx, name, "AIMAN_ID")
			id = strings.TrimSpace(id)
			if iderr != nil || id == "" {
				continue
			}
			sess := domain.Session{
				ID:          id,
				TmuxSession: name,
				Backend:     domain.BackendTmux,
				Status:      domain.SessionStatusActive,
			}
			if cwd, cerr := s.remote.GetTmuxSessionCWD(ctx, name); cerr == nil {
				sess.WorkingDirectory = strings.TrimSpace(cwd)
			}
			out = append(out, sess)
		}
	}

	// PTY-hosted sessions carry the aiman id directly.
	if recs, perr := usecase.ScanPTYSessions(ctx, s.remote); perr == nil {
		for _, rec := range recs {
			id := strings.TrimSpace(rec.ID)
			if id == "" || rec.Status != "running" {
				continue
			}
			out = append(out, domain.Session{
				ID:               id,
				TmuxSession:      rec.Name,
				Backend:          domain.BackendPTY,
				WorkingDirectory: rec.Dir,
				Status:           domain.SessionStatusActive,
			})
		}
	}
	return out
}

// mergeLiveSessions folds what is running on this host into the stored list.
// Stored records win for identity the host cannot know (name, group, issue,
// branch, repo); liveness and terminal facts come from the host.
func mergeLiveSessions(stored, live []domain.Session) []domain.Session {
	byID := make(map[string]int, len(stored))
	for i := range stored {
		byID[stored[i].ID] = i
	}
	out := stored
	for _, l := range live {
		if i, ok := byID[l.ID]; ok {
			if l.TmuxSession != "" {
				out[i].TmuxSession = l.TmuxSession
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

	// Then fold in what is running here. Sessions absent from the local
	// database are labelled in memory only: this host is not their owner, so
	// writing rows for them would put identity in two places and let it drift.
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
// It deliberately does not use backfill/AssignSessionName: those serve the
// creation flow, where the first session earns the friendly name "impl" and a
// long tmux name fails ValidateSessionName and is discarded. That turned six
// distinct sessions into impl, impl-2 … impl-6 — unidentifiable, and useless
// for addressing a specific sibling. The tmux/PTY session name is the label
// already shown in the dashboard, so it is what an agent should be able to use.
func labelDiscovered(sess *domain.Session, others []domain.Session) {
	if sess.IssueKey == "" {
		sess.IssueKey = domain.ExtractKey(sess.TmuxSession)
	}
	if sess.Group == "" {
		sess.Group = domain.AssignSessionGroup("", sess.IssueKey, sess.RepoName, false)
	}
	if sess.Name != "" {
		return
	}
	name := strings.TrimSpace(sess.TmuxSession)
	if name == "" {
		name = sess.ID
	}
	// Names address sessions, so they must stay distinct even if two hosts ever
	// present the same tmux name.
	base := name
	for n := 2; domain.NameTaken(others, name); n++ {
		name = fmt.Sprintf("%s-%d", base, n)
	}
	sess.Name = name
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
		preferred := sess.TmuxSession
		name, err := domain.AssignSessionName(others, preferred, false)
		if err != nil {
			name = "s1"
		}
		sess.Name = name
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
	text := ""
	if sess.IsPTY() && s.pty != nil {
		screen, cerr := s.pty.CaptureScreen(sess.ID)
		if cerr != nil {
			return errResp(req.ID, CodeInvalidParams, cerr.Error())
		}
		if params.Lines > 0 {
			screen = tailLines(screen, params.Lines)
		}
		return Response{ID: req.ID, Result: map[string]any{
			"type":    "pane_read",
			"session": sessionInfo(sess, req.Caller),
			"text":    screen,
		}}
	}
	if s.remote != nil {
		var err error
		if sess.IsPTY() {
			text, err = usecase.CapturePTYPane(ctx, s.remote, sess.ID, params.Lines)
		} else {
			text, err = s.remote.CaptureTmuxPane(ctx, sess.TmuxSession)
		}
		if err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	if params.Lines > 0 {
		lines := strings.Split(text, "\n")
		if len(lines) > params.Lines {
			text = strings.Join(lines[len(lines)-params.Lines:], "\n")
		}
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type":    "pane_read",
		"session": sessionInfo(sess, req.Caller),
		"text":    text,
	}}
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
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
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

func (s *Server) handleCreate(ctx context.Context, req Request) Response {
	var params struct {
		Name   string `json:"name"`
		Group  string `json:"group"`
		Parent string `json:"parent"`
		Orphan bool   `json:"orphan"`
		Quick  bool   `json:"quick"`
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
		Agent  string `json:"agent"`
		Dir    string `json:"dir"`
		Prompt string `json:"prompt"`
		Issue  string `json:"issue"`
		Base   string `json:"base"`
		// Backend picks the terminal runtime ("tmux" or "pty"). The dashboard
		// offers this per session, so a remote-side create has to carry it or a
		// pty session would silently come up under tmux.
		Backend string `json:"backend"`
		// ExistingBranch starts from a branch that already exists on the remote
		// instead of creating one.
		ExistingBranch bool `json:"existing_branch"`
		// AgentName is the agent's display name when it differs from the command
		// ("Claude Code" against "claude"). Absent means the command is the name.
		AgentName string `json:"agent_name"`
		// PromptFree suppresses the task file the skill engine writes from the
		// JIRA issue. A pointer because absent has to keep meaning "true": that is
		// what an agent creating an ad-hoc sibling wants, and it was the only
		// behaviour before callers could ask for a task-driven session.
		PromptFree *bool `json:"prompt_free"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	if strings.TrimSpace(params.Agent) == "" {
		return errResp(req.ID, CodeInvalidParams, "agent is required")
	}
	if !params.Quick && (params.Repo == "" || params.Branch == "") {
		return errResp(req.ID, CodeInvalidParams, "repo, branch, and agent are required (or --quick)")
	}
	existing, err := s.loadSessions(ctx)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
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
	parentID, perr := domain.ResolveCreateParent(params.Orphan, params.Parent, req.Caller, "", existing)
	if perr != nil {
		return errResp(req.ID, CodeInvalidParams, perr.Error())
	}
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
		BaseBranch:    params.Base,
		// The agent's launch model and effort come from this host's own agent
		// defaults, which serve install keeps in step with the laptop's config.
		Agent:          &domain.Agent{Name: agentName, Command: params.Agent},
		Repo:           domain.Repo{Name: params.Repo},
		SessionBackend: domain.ResolveSessionBackend(params.Backend, ""),
		ExistingBranch: params.ExistingBranch,
	}
	var caller *domain.Session
	if found, ok := resolveSession(existing, req.Caller); ok {
		caller = &found
	}
	if root := createGitRoot(caller, existing); root != "" {
		cfg.SSHManager = local.NewExecutor(root)
	}
	sess, err := s.create.CreateSession(ctx, cfg)
	if err != nil {
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	sess.Name = name
	sess.Group = group
	sess.ParentID = parentID
	if s.repo != nil {
		if err := s.repo.Save(ctx, sess); err != nil {
			return errResp(req.ID, CodeCreateFailed, err.Error())
		}
	}
	info := sessionInfo(*sess, req.Caller)
	return Response{ID: req.ID, Result: map[string]any{"type": "session", "session": info}}
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
	}
	if sessionID == "" || (rep.ID == "" && rep.State == "" && !rep.Ended && rep.Title == "") {
		return errResp(req.ID, CodeInvalidParams, "id plus agent_session_id, state, title, or ended is required")
	}
	if s.repo != nil {
		sess, err := s.repo.Get(ctx, sessionID)
		if err == nil {
			agenthook.ApplyReport(sess, rep, time.Now())
			if err := s.repo.Save(ctx, sess); err != nil {
				return errResp(req.ID, CodeInvalidParams, err.Error())
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
		if s.TmuxSession == target {
			return s, true
		}
	}
	return domain.Session{}, false
}
