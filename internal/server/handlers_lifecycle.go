package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	infraAgent "github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/usecase"
	"github.com/bouwerp/aiman/internal/worktree"
)

const (
	// SessionCreateWait is how long a client waits for session.create, which
	// clones, starts the agent and delivers its prompt.
	SessionCreateWait = 5 * time.Minute
	// SessionRestartTimeout bounds one session.restart.
	SessionRestartTimeout = 2 * time.Minute
	// SessionTerminateTimeout bounds one session.terminate.
	SessionTerminateTimeout = 90 * time.Second
	// restartHandoffTimeout leaves the rest of SessionRestartTimeout for the
	// agent's boot and its prompt.
	restartHandoffTimeout = 45 * time.Second
	// handoverWriteTimeout caps the handover teardown and restart write.
	handoverWriteTimeout = 20 * time.Second
)

// IgnoreEnsurer adds aiman's ignore rule to a worktree's .gitignore.
type IgnoreEnsurer interface {
	EnsureAimanSessionFilesGitignored(ctx context.Context, remote domain.RemoteExecutor, worktreePath string) error
}

// RestartDeps is what session.restart needs beyond the session.
type RestartDeps struct {
	Config  *config.Config
	Engine  domain.SkillEngine
	Ignorer IgnoreEnsurer
}

// SetRestartDeps attaches session.restart's launch preparation.
func (s *Server) SetRestartDeps(d RestartDeps) {
	s.restart = d
}

// WorktreeDirtyData is the data of a worktree_dirty refusal: what removing the
// worktree would lose.
type WorktreeDirtyData struct {
	ChangedFiles    int      `json:"changed_files"`
	Paths           []string `json:"paths"`
	UnpushedCommits int      `json:"unpushed_commits"`
}

// dispatchLifecycle routes the methods the phone runs sessions and the host with.
func (s *Server) dispatchLifecycle(ctx context.Context, req Request) Response {
	switch req.Method {
	case "session.restart":
		return s.handleSessionRestart(ctx, req)
	case "session.terminate":
		return s.handleSessionTerminate(ctx, req)
	case "session.land":
		return s.handleSessionLand(ctx, req)
	case "repo.list":
		return s.handleRepoList(ctx, req)
	case "host.version":
		return s.handleHostVersion(ctx, req)
	case "host.update":
		return s.handleHostUpdate(ctx, req)
	case "resources.get":
		return s.handleResourcesGet(ctx, req)
	case "session.limits":
		return s.handleSessionLimits(ctx, req)
	default:
		return s.handleHostStats(ctx, req)
	}
}

// lockedTarget resolves target, takes the session's lock and resolves it again,
// so a call that waited behind a terminate reads not_found instead of acting on
// a row that is gone. On failure the Response is the answer and unlock is nil.
func (s *Server) lockedTarget(ctx context.Context, req Request, target string) (domain.Session, []domain.Session, func(), *Response) {
	fail := func(code, msg string) (domain.Session, []domain.Session, func(), *Response) {
		r := errResp(req.ID, code, msg)
		return domain.Session{}, nil, nil, &r
	}
	if target == "" {
		return fail(CodeInvalidParams, "session is required")
	}
	list, err := s.loadSessions(ctx)
	if err != nil {
		return fail(CodeInvalidParams, err.Error())
	}
	first, ok := resolveSession(list, target)
	if !ok {
		return fail(CodeNotFound, "session not found")
	}
	unlock := s.lockSession(first.ID)
	list, err = s.loadSessions(ctx)
	if err != nil {
		unlock()
		return fail(CodeInvalidParams, err.Error())
	}
	for i, sess := range list {
		if sess.ID != first.ID {
			continue
		}
		// The list is cached for identity only; a restart the previous call
		// made (launch model, effort, vendor id, ended) is in the row.
		if fresh, ok := s.sessionByID(ctx, sess.ID); ok {
			sess = fresh
			list[i] = fresh
		}
		return sess, list, unlock, nil
	}
	unlock()
	return fail(CodeNotFound, "session not found")
}

var (
	agentCommandRe = regexp.MustCompile(`^[A-Za-z0-9._/+-]+( [A-Za-z0-9._/+-]+)?$`)
	launchValueRe  = regexp.MustCompile(`^[A-Za-z0-9._:/@+=\[\]-]+$`)
)

// restartAgent resolves the requested agent to its command: a display name
// ("Claude Code") or a command both work, and it must be installed here. The
// value ends up in a shell, so anything but a plain command is refused.
func (s *Server) restartAgent(ctx context.Context, requested string) (string, error) {
	command := strings.TrimSpace(requested)
	if known, ok := infraAgent.FindKnown(command); ok {
		command = known.Command
	}
	if !agentCommandRe.MatchString(command) {
		return "", fmt.Errorf("agent %q is not a command", requested)
	}
	if s.agentInstalled == nil || !s.agentInstalled(ctx, command) {
		return "", fmt.Errorf("agent %q is not installed on this host", command)
	}
	return command, nil
}

func (s *Server) handleSessionRestart(ctx context.Context, req Request) Response {
	ctx, cancel := context.WithTimeout(ctx, SessionRestartTimeout)
	defer cancel()
	if s.isSessionCall(ctx, req) {
		return errResp(req.ID, CodeNeedsOperator, "only the operator may restart a session")
	}
	var p struct {
		Agent  string `json:"agent"`
		Model  string `json:"model"`
		Effort string `json:"effort"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	p.Model, p.Effort = strings.TrimSpace(p.Model), strings.TrimSpace(p.Effort)
	if strings.TrimSpace(p.Agent) == "" {
		return errResp(req.ID, CodeInvalidParams, "agent is required")
	}
	for _, v := range []string{p.Model, p.Effort} {
		if v != "" && !launchValueRe.MatchString(v) {
			return errResp(req.ID, CodeInvalidParams, fmt.Sprintf("%q is not a model or effort value", v))
		}
	}
	sess, _, unlock, bad := s.lockedTarget(ctx, req, sessionTarget(req.Params))
	if bad != nil {
		return *bad
	}
	defer unlock()
	if sess.Role.Hidden() {
		return errResp(req.ID, CodeInvalidParams, "aiman manages this helper session itself")
	}
	if sess.IsLegacyTmux() {
		return legacyTerminalResp(req.ID, sess)
	}
	command, err := s.restartAgent(ctx, p.Agent)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	if s.remote == nil {
		return errResp(req.ID, CodeCreateFailed, "restart is not configured")
	}
	restarted, err := s.restartSession(ctx, sess, command, p.Model, p.Effort)
	if err != nil {
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	info := sessionInfo(restarted, req.Caller)
	s.decorateState(ctx, &info, restarted)
	return Response{ID: req.ID, Result: map[string]any{"type": "session", "session": info}}
}

// restartSession runs the shared restart and saves the row. The handover reads
// the session as it was: its transcript belongs to the outgoing agent.
func (s *Server) restartSession(ctx context.Context, before domain.Session, command, model, effort string) (domain.Session, error) {
	sess := before
	switched := usecase.AdoptRestartAgent(&sess, command)
	if model != "" {
		sess.LaunchModel = model
	}
	if effort != "" {
		sess.LaunchEffort = effort
	}
	secrets, err := s.repo.ListSecrets(ctx)
	if err != nil {
		log.Printf("session.restart: list secrets: %v", err)
	}
	restartReq := usecase.RestartRequest{
		Cfg:            s.restart.Config,
		Session:        &sess,
		HandoffTimeout: restartHandoffTimeout,
		NoUpdateGuard:  s.restart.Config != nil && !s.restart.Config.AgentUpdateGuards(),
		SessionCfg: domain.SessionConfig{
			Agent:      &domain.Agent{Name: command, Command: command},
			FreshAgent: switched,
			EnvSecrets: secrets,
		},
		WriteHandover: func(ctx context.Context, reason, agent string) (usecase.HandoverResult, error) {
			ctx, cancel := context.WithTimeout(ctx, handoverWriteTimeout)
			defer cancel()
			res, err := s.writeHandover(ctx, before, reason, agent)
			return usecase.HandoverResult{Path: res.Path, Written: res.Written, Exists: res.Path != ""}, err
		},
		Log: func(format string, args ...any) {
			log.Printf("session.restart %s: "+format, append([]any{before.ID}, args...)...)
		},
	}
	if s.restart.Ignorer != nil {
		restartReq.EnsureIgnored = func(ctx context.Context, root string) error {
			return s.restart.Ignorer.EnsureAimanSessionFilesGitignored(ctx, s.remote, root)
		}
	}
	if clearer, ok := s.repo.(interface {
		ClearNativeAgentIdentity(context.Context, string) error
	}); ok {
		restartReq.ClearIdentity = clearer.ClearNativeAgentIdentity
	}
	if err := usecase.RestartSession(ctx, s.remote, s.restart.Engine, restartReq); err != nil {
		return domain.Session{}, err
	}
	sess.AgentEnded = false
	if err := s.repo.Save(ctx, &sess); err != nil {
		return domain.Session{}, fmt.Errorf("agent restarted, but saving the session failed: %w", err)
	}
	// Save cannot retract a stored SessionEnd, and this process is alive.
	if clr, ok := s.repo.(agentEndedClearer); ok {
		if err := clr.ClearAgentEnded(ctx, sess.ID); err != nil {
			log.Printf("session.restart %s: clear ended: %v", sess.ID, err)
		}
	}
	s.appendEvent(ctx, sess.ID, domain.EventActorUser, domain.EventSessionRestarted, map[string]any{
		"agent": command, "model": sess.LaunchModel, "effort": sess.LaunchEffort, "switched": switched,
	})
	return sess, nil
}

func (s *Server) handleSessionTerminate(ctx context.Context, req Request) Response {
	ctx, cancel := context.WithTimeout(ctx, SessionTerminateTimeout)
	defer cancel()
	scope, isSession := s.sessionScopeOf(ctx, req)
	if isSession && scope.caller == "" {
		return errResp(req.ID, CodeNeedsOperator, "only the operator or a session's parent may terminate a session")
	}
	var p struct {
		Discard bool `json:"discard"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	sess, list, unlock, bad := s.lockedTarget(ctx, req, sessionTarget(req.Params))
	if bad != nil {
		return *bad
	}
	defer unlock()
	// A session may end its own descendants, never itself: the call would
	// stop the agent that is waiting for the answer.
	if isSession {
		if sess.ID == scope.caller {
			return errResp(req.ID, CodeNeedsOperator, "a session cannot terminate itself")
		}
		if !scope.descends(sess.ID) {
			return scopeRefusal(req.ID, scope, "session "+sess.ID)
		}
	}
	if sess.Role.Hidden() {
		return errResp(req.ID, CodeInvalidParams, "aiman manages this helper session itself")
	}
	// aiman never stops tmux, and removing a worktree under a tmux agent that
	// may still run needs the TUI's explicit confirmation.
	if sess.IsLegacyTmux() {
		return legacyTerminalResp(req.ID, sess)
	}
	plan := s.planWorktreeRemoval(ctx, sess, list)
	if plan.removable() && !p.Discard {
		loss, err := worktree.Pending(ctx, plan.info)
		if err != nil {
			return errResp(req.ID, CodeInvalidParams, fmt.Sprintf("cannot read worktree %s: %v", plan.info.Root, err))
		}
		if !loss.Empty() {
			return worktreeDirtyResp(req.ID, plan.info, loss)
		}
	}

	hctx, hcancel := context.WithTimeout(ctx, handoverWriteTimeout)
	if _, err := s.writeHandover(hctx, sess, "forget"); err != nil {
		log.Printf("session.terminate %s: handover: %v", sess.ID, err)
	}
	hcancel()

	s.retireHelpers(ctx, sess, list)
	term := serverTerminal{s}
	if err := term.Stop(ctx, sess); err != nil {
		log.Printf("session.terminate %s: stop: %v", sess.ID, err)
	}
	if term.Alive(ctx, sess) {
		return errResp(req.ID, CodeTimeout, "the agent did not stop; nothing was removed")
	}

	removed, note := s.removeWorktree(ctx, plan, p.Discard)
	if err := s.dropSession(ctx, sess); err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	s.appendEvent(ctx, sess.ID, domain.EventActorUser, domain.EventSessionEnded, map[string]any{
		"reason": "terminate", "worktree_removed": removed, "discard": p.Discard,
	})
	return Response{ID: req.ID, Result: terminatedResult(sess, list, plan, removed, note)}
}

func worktreeDirtyResp(id string, info worktree.Info, loss worktree.Loss) Response {
	msg := fmt.Sprintf("worktree %s has %d changed files and %d unpushed commits", info.Root, loss.ChangedFiles, loss.UnpushedCommits)
	paths := loss.Paths
	if paths == nil {
		paths = []string{}
	}
	return Response{ID: id, Error: &Error{Code: CodeWorktreeDirty, Message: msg, Data: WorktreeDirtyData{
		ChangedFiles: loss.ChangedFiles, Paths: paths, UnpushedCommits: loss.UnpushedCommits,
	}}}
}

// terminatedResult is the session_terminated answer. worktree_note says why a
// worktree stayed, and children_left names the running children that now have
// no parent, as the TUI's confirm dialog warns.
func terminatedResult(sess domain.Session, list []domain.Session, plan worktreePlan, removed bool, note string) map[string]any {
	branch := plan.info.Branch
	if branch == "" {
		branch = sess.Branch
	}
	result := map[string]any{
		"type": "session_terminated", "session": sess.ID, "worktree_removed": removed, "branch": branch,
	}
	if note != "" {
		result["worktree_note"] = note
	}
	var left []string
	for _, c := range list {
		if c.ParentID == sess.ID && !c.Role.Hidden() && !c.AgentEnded {
			left = append(left, c.ID)
		}
	}
	if len(left) > 0 {
		result["children_left"] = left
	}
	return result
}

// worktreePlan is what teardown may do with a session's worktree. keep says why
// it stays; an empty keep means it is this session's own linked worktree.
type worktreePlan struct {
	info worktree.Info
	keep string
}

func (p worktreePlan) removable() bool { return p.keep == "" }

// planWorktreeRemoval decides before anything stops. Only a linked worktree no
// other session uses may go, so a dirty main checkout or a shared tree is not
// refused either: terminating loses nothing in it.
func (s *Server) planWorktreeRemoval(ctx context.Context, sess domain.Session, list []domain.Session) worktreePlan {
	if strings.TrimSpace(sess.WorktreePath) == "" {
		return worktreePlan{keep: "the session has no worktree"}
	}
	info := worktree.Inspect(ctx, sess.WorktreePath)
	switch info.Kind {
	case worktree.KindMain:
		return worktreePlan{info: info, keep: "it is the repository's main checkout"}
	case worktree.KindLinked:
	default:
		return worktreePlan{info: info, keep: "it is not a linked git worktree"}
	}
	if other, shared := worktreeUser(info.Root, sess, list); shared {
		return worktreePlan{info: info, keep: "session " + other + " also uses it"}
	}
	return worktreePlan{info: info}
}

// worktreeUser names another session working in root. A row with no running
// agent counts: it can be revived and needs its tree. Hidden helpers of sess go
// down with it, so they do not.
func worktreeUser(root string, sess domain.Session, list []domain.Session) (string, bool) {
	for _, other := range list {
		if other.ID == sess.ID || (other.ParentID == sess.ID && other.Role.Hidden()) {
			continue
		}
		if !pathWithin(other.WorktreePath, root) && !pathWithin(other.WorkingDirectory, root) {
			continue
		}
		for _, label := range []string{other.Name, other.TerminalName, other.ID} {
			if label != "" {
				return label, true
			}
		}
	}
	return "", false
}

func pathWithin(p, root string) bool {
	p = strings.TrimSpace(p)
	if p == "" {
		return false
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	rel, err := filepath.Rel(root, p)
	return err == nil && filepath.IsLocal(rel)
}

// retireHelpers ends what would otherwise outlive sess. A consult is retired,
// because the tracker only sweeps consults of parents it still lists. A
// reviewer is stopped, and its review's watcher finishes and cleans up. A
// handover child is left to finish: it stores the note and memory job from the
// handover just written, and cleans up after itself.
func (s *Server) retireHelpers(ctx context.Context, sess domain.Session, list []domain.Session) {
	for _, kid := range list {
		if kid.ParentID != sess.ID {
			continue
		}
		switch kid.Role {
		case domain.SessionRoleConsult:
			s.RetireConsult(ctx, kid)
		case domain.SessionRoleReviewer:
			s.stopReviewer(ctx, kid)
		}
	}
}

func (s *Server) stopReviewer(ctx context.Context, reviewer domain.Session) {
	if err := (serverTerminal{s}).Stop(ctx, reviewer); err != nil {
		log.Printf("session.terminate: stop reviewer %s: %v", reviewer.ID, err)
	}
	if s.verifier != nil {
		return
	}
	if err := s.dropSession(ctx, reviewer); err != nil {
		log.Printf("session.terminate: forget reviewer %s: %v", reviewer.ID, err)
	}
}

// removeWorktree runs last, with the agent gone. Without discard it reads the
// tree again, because the agent could work right up to the stop. A worktree that
// cannot go stays, with the reason: the session still ends and nothing is lost.
func (s *Server) removeWorktree(ctx context.Context, plan worktreePlan, discard bool) (bool, string) {
	if !plan.removable() {
		return false, plan.keep
	}
	if !discard {
		loss, err := worktree.Pending(ctx, plan.info)
		if err != nil {
			return false, "cannot read it: " + err.Error()
		}
		if !loss.Empty() {
			return false, fmt.Sprintf("the agent left %d changed files and %d unpushed commits before it stopped", loss.ChangedFiles, loss.UnpushedCommits)
		}
	}
	if err := worktree.Remove(ctx, plan.info, discard); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return false, "git timed out"
		}
		return false, err.Error()
	}
	return true, ""
}
