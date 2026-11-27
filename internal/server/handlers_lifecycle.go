package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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
	SessionTerminateTimeout = 3 * time.Minute
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
	// Session is the session whose worktree it is: the target or one of the
	// descendants that would go with it.
	Session         string   `json:"session"`
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
	case "session.adopt_scope":
		return s.handleSessionAdoptScope(ctx, req)
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
		// KeepChildren leaves visible descendants running with no parent.
		KeepChildren bool `json:"keep_children"`
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
	return s.terminateTree(ctx, req.ID, sess, list, p.Discard, p.KeepChildren, domain.EventActorUser)
}

// TerminateLanded ends a child whose work landed on its coordinator's branch,
// as an operator's session.terminate without discard would: its descendants
// go too, and a worktree holding unsaved work refuses it. The coordinator
// tracker calls it once it marks the child's item done.
func (s *Server) TerminateLanded(ctx context.Context, childID string) error {
	ctx, cancel := context.WithTimeout(ctx, SessionTerminateTimeout)
	defer cancel()
	unlock := s.lockSession(childID)
	defer unlock()
	list, err := s.loadSessions(ctx)
	if err != nil {
		return err
	}
	sess, ok := s.sessionByID(ctx, childID)
	if !ok {
		return nil
	}
	for i := range list {
		if list[i].ID == sess.ID {
			list[i] = sess
		}
	}
	if sess.Role.Hidden() {
		return fmt.Errorf("session %s is a helper", childID)
	}
	resp := s.terminateTree(ctx, "", sess, list, false, false, domain.EventActorAiman)
	if resp.Error != nil {
		return errors.New(resp.Error.Message)
	}
	return nil
}

// terminateTree is session.terminate after the caller is admitted and sess is
// locked: sess and, unless keepChildren, every visible descendant.
func (s *Server) terminateTree(ctx context.Context, reqID string, sess domain.Session, list []domain.Session, discard, keepChildren bool, actor domain.EventActor) Response {
	// aiman never stops tmux, and removing a worktree under a tmux agent that
	// may still run needs the TUI's explicit confirmation.
	if sess.IsLegacyTmux() {
		return legacyTerminalResp(reqID, sess)
	}
	var kids []domain.Session
	if !keepChildren {
		var unlockKids func()
		kids, unlockKids = s.lockDescendants(ctx, sess, list)
		defer unlockKids()
	}
	for _, kid := range kids {
		if kid.IsLegacyTmux() {
			return legacyTerminalResp(reqID, kid)
		}
	}
	// Deepest first: every descendant goes before the session it hangs off.
	going := append(append([]domain.Session(nil), kids...), sess)
	plans, bad := s.planTeardown(ctx, reqID, going, list, discard)
	if bad != nil {
		return *bad
	}

	s.writeForgetHandovers(ctx, going)
	term := serverTerminal{s}
	for _, g := range going {
		s.retireHelpers(ctx, g, list)
		if err := term.Stop(ctx, g); err != nil {
			log.Printf("session.terminate %s: stop: %v", g.ID, err)
		}
	}
	for _, g := range going {
		if term.Alive(ctx, g) {
			return errResp(reqID, CodeTimeout, fmt.Sprintf("the agent of session %s did not stop; nothing was removed", sessionLabel(g)))
		}
	}

	var result map[string]any
	var ended []string
	for i, g := range going {
		out := s.removeTree(ctx, plans[i], discard)
		if err := s.dropSession(ctx, g, "terminate"); err != nil {
			return errResp(reqID, CodeInvalidParams, fmt.Sprintf("session %s: %v", sessionLabel(g), err))
		}
		payload := map[string]any{"reason": "terminate", "worktree_removed": out.removed, "discard": discard}
		if g.ID != sess.ID {
			payload["terminated_with"] = sess.ID
			ended = append(ended, g.ID)
		}
		s.appendEvent(ctx, g.ID, actor, domain.EventSessionEnded, payload)
		if g.ID == sess.ID {
			result = terminatedResult(sess, plans[i], out)
		}
	}
	if len(ended) > 0 {
		result["children_terminated"] = ended
	}
	if keepChildren {
		if left := runningChildren(sess, list); len(left) > 0 {
			result["children_left"] = left
		}
	}
	return Response{ID: reqID, Result: result}
}

// lockDescendants returns every visible session below sess, deepest first, each
// re-read under its own lock. Locks are taken parent before child, the order
// every terminate takes them in, so two cascades cannot wait on each other.
// Hidden helpers are not listed: retireHelpers ends them by their own rules.
func (s *Server) lockDescendants(ctx context.Context, sess domain.Session, list []domain.Session) ([]domain.Session, func()) {
	byParent := map[string][]domain.Session{}
	for _, c := range list {
		if c.ParentID != "" && c.ParentID != c.ID && !c.Role.Hidden() {
			byParent[c.ParentID] = append(byParent[c.ParentID], c)
		}
	}
	var unlocks []func()
	seen := map[string]bool{sess.ID: true}
	var walk func(id string) []domain.Session
	walk = func(id string) []domain.Session {
		var out []domain.Session
		for _, kid := range byParent[id] {
			if seen[kid.ID] {
				continue
			}
			seen[kid.ID] = true
			unlocks = append(unlocks, s.lockSession(kid.ID))
			below := walk(kid.ID)
			// A concurrent terminate may have ended it while this one waited.
			fresh, ok := s.sessionByID(ctx, kid.ID)
			if !ok {
				out = append(out, below...)
				continue
			}
			out = append(append(out, below...), fresh)
		}
		return out
	}
	kids := walk(sess.ID)
	return kids, func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}
}

// planTeardown decides each session's worktree before anything stops, and
// refuses the whole call when one removable worktree would lose work. Sessions
// that go together do not keep each other's tree, and a tree they share is
// removed once, by the highest session that has it.
func (s *Server) planTeardown(ctx context.Context, reqID string, going, list []domain.Session, discard bool) ([]worktreePlan, *Response) {
	gone := make(map[string]bool, len(going))
	for _, g := range going {
		gone[g.ID] = true
	}
	staying := make([]domain.Session, 0, len(list))
	for _, c := range list {
		// A going session's hidden helpers go with it.
		if !gone[c.ID] && (!c.Role.Hidden() || !gone[c.ParentID]) {
			staying = append(staying, c)
		}
	}
	claimed := map[string]string{}
	plans := make([]worktreePlan, len(going))
	// The target is last in going and plans first, so it claims a tree it
	// shares with a child.
	for i := len(going) - 1; i >= 0; i-- {
		g := going[i]
		plan := s.planWorktreeRemoval(ctx, g, staying)
		claim := func(p *worktreePlan) {
			if !p.removable() {
				return
			}
			if owner, dup := claimed[p.info.Root]; dup {
				p.keep = "session " + owner + " removes it"
			} else {
				claimed[p.info.Root] = sessionLabel(g)
			}
		}
		claim(&plan)
		for j := range plan.members {
			claim(&plan.members[j].plan)
		}
		plans[i] = plan
		if discard {
			continue
		}
		loss, root, err := planLoss(ctx, plan)
		if err != nil {
			r := errResp(reqID, CodeInvalidParams, fmt.Sprintf("cannot read worktree %s: %v", root, err))
			return nil, &r
		}
		if !loss.Empty() {
			r := worktreeDirtyResp(reqID, g, root, loss)
			return nil, &r
		}
	}
	return plans, nil
}

// planLoss reads what removing plan's worktrees would lose. A workspace
// session's members add up, each path prefixed with its member's directory
// name. root names the tree in a message: the failing member on an error.
func planLoss(ctx context.Context, plan worktreePlan) (worktree.Loss, string, error) {
	if plan.wsDir == "" {
		if !plan.removable() {
			return worktree.Loss{}, plan.info.Root, nil
		}
		loss, err := worktree.Pending(ctx, plan.info)
		return loss, plan.info.Root, err
	}
	var total worktree.Loss
	for _, m := range plan.members {
		if !m.plan.removable() {
			continue
		}
		loss, err := worktree.Pending(ctx, m.plan.info)
		if err != nil {
			return worktree.Loss{}, m.plan.info.Root, err
		}
		total.ChangedFiles += loss.ChangedFiles
		total.UnpushedCommits += loss.UnpushedCommits
		for _, p := range loss.Paths {
			if len(total.Paths) < worktree.MaxLossPaths {
				total.Paths = append(total.Paths, m.dir()+"/"+p)
			}
		}
	}
	return total, plan.wsDir, nil
}

// writeForgetHandovers writes every going session's handover at once, under one
// shared cap, so a deep tree does not spend the call's limit on handovers.
func (s *Server) writeForgetHandovers(ctx context.Context, going []domain.Session) {
	hctx, cancel := context.WithTimeout(ctx, handoverWriteTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, g := range going {
		wg.Add(1)
		go func(g domain.Session) {
			defer wg.Done()
			if _, err := s.writeHandover(hctx, g, "forget"); err != nil {
				log.Printf("session.terminate %s: handover: %v", g.ID, err)
			}
		}(g)
	}
	wg.Wait()
}

func worktreeDirtyResp(id string, sess domain.Session, root string, loss worktree.Loss) Response {
	msg := fmt.Sprintf("worktree %s of session %s has %d changed files and %d unpushed commits",
		root, sessionLabel(sess), loss.ChangedFiles, loss.UnpushedCommits)
	paths := loss.Paths
	if paths == nil {
		paths = []string{}
	}
	return Response{ID: id, Error: &Error{Code: CodeWorktreeDirty, Message: msg, Data: WorktreeDirtyData{
		Session: sess.ID, ChangedFiles: loss.ChangedFiles, Paths: paths, UnpushedCommits: loss.UnpushedCommits,
	}}}
}

// runningChildren names sess's visible children whose agent still runs: what
// keep_children leaves with no parent.
func runningChildren(sess domain.Session, list []domain.Session) []string {
	var left []string
	for _, c := range list {
		if c.ParentID == sess.ID && !c.Role.Hidden() && !c.AgentEnded {
			left = append(left, c.ID)
		}
	}
	return left
}

// terminatedResult is the session_terminated answer. worktree_note says why a
// worktree stayed. A workspace session adds repos, one entry per member.
func terminatedResult(sess domain.Session, plan worktreePlan, out treeRemoval) map[string]any {
	branch := plan.info.Branch
	if branch == "" {
		branch = sess.Branch
	}
	result := map[string]any{
		"type": "session_terminated", "session": sess.ID, "worktree_removed": out.removed, "branch": branch,
	}
	if out.note != "" {
		result["worktree_note"] = out.note
	}
	if out.repos != nil {
		result["repos"] = out.repos
	}
	return result
}

// worktreePlan is what teardown may do with a session's worktree. keep says why
// it stays; an empty keep means it is this session's own linked worktree. A
// workspace session's plan has wsDir set and one plan per member instead.
type worktreePlan struct {
	info    worktree.Info
	keep    string
	wsDir   string
	members []memberPlan
}

// memberPlan is one member worktree of a workspace session.
type memberPlan struct {
	repo domain.SessionRepo
	plan worktreePlan
}

// dir is the member's directory name inside the workspace directory.
func (m memberPlan) dir() string { return filepath.Base(m.repo.WorktreePath) }

func (p worktreePlan) removable() bool { return p.keep == "" }

// planWorktreeRemoval decides before anything stops. Only a linked worktree no
// other session uses may go, so a dirty main checkout or a shared tree is not
// refused either: terminating loses nothing in it.
func (s *Server) planWorktreeRemoval(ctx context.Context, sess domain.Session, list []domain.Session) worktreePlan {
	if sess.IsWorkspace() {
		plan := worktreePlan{wsDir: sess.WorktreePath, keep: "it is a workspace directory"}
		for _, m := range sess.Members {
			plan.members = append(plan.members, memberPlan{repo: m, plan: planTreeRemoval(ctx, sess, m.WorktreePath, list)})
		}
		return plan
	}
	return planTreeRemoval(ctx, sess, sess.WorktreePath, list)
}

// planTreeRemoval plans the one worktree at path.
func planTreeRemoval(ctx context.Context, sess domain.Session, path string, list []domain.Session) worktreePlan {
	if strings.TrimSpace(path) == "" {
		return worktreePlan{keep: "the session has no worktree"}
	}
	info := worktree.Inspect(ctx, path)
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
		if !pathWithin(other.WorktreePath, root) && !pathWithin(other.WorkingDirectory, root) && !memberWithin(other, root) {
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

// memberWithin reports a member worktree of a workspace session inside root.
func memberWithin(sess domain.Session, root string) bool {
	for _, m := range sess.Members {
		if pathWithin(m.WorktreePath, root) {
			return true
		}
	}
	return false
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
	if err := s.dropSession(ctx, reviewer, "reviewer_stopped"); err != nil {
		log.Printf("session.terminate: forget reviewer %s: %v", reviewer.ID, err)
	}
}

// treeRemoval is what teardown did with a session's worktrees. repos is set
// for a workspace session only.
type treeRemoval struct {
	removed bool
	note    string
	repos   []map[string]any
}

// removeTree removes a session's worktree, or for a workspace session each member
// worktree and then the workspace directory. The directory goes only when
// nothing but aiman's scaffolding is left in it, and removed is true only when
// every member and the directory went.
func (s *Server) removeTree(ctx context.Context, plan worktreePlan, discard bool) treeRemoval {
	if plan.wsDir == "" {
		removed, note := s.removeWorktree(ctx, plan, discard)
		return treeRemoval{removed: removed, note: note}
	}
	out := treeRemoval{removed: true, repos: []map[string]any{}}
	for _, m := range plan.members {
		removed, note := s.removeWorktree(ctx, m.plan, discard)
		branch := m.repo.Branch
		if branch == "" {
			branch = m.plan.info.Branch
		}
		entry := map[string]any{"repo": m.repo.Repo, "branch": branch, "worktree_removed": removed}
		if note != "" {
			entry["worktree_note"] = note
		}
		out.repos = append(out.repos, entry)
		out.removed = out.removed && removed
	}
	if !removeWorkspaceDir(ctx, plan.wsDir) {
		out.removed = false
		out.note = "the workspace directory " + plan.wsDir + " still holds files"
	}
	if !out.removed && out.note == "" {
		out.note = "some member worktrees stayed; see repos"
	}
	return out
}

// removeWorkspaceDir clears aiman's scaffolding from the workspace directory
// and removes it when that left it empty. It reports whether the directory is
// gone.
func removeWorkspaceDir(ctx context.Context, ws string) bool {
	if strings.TrimSpace(ws) == "" {
		return true
	}
	if _, err := os.Lstat(ws); errors.Is(err, os.ErrNotExist) {
		return true
	}
	// rmdir refuses a directory with anything else in it, which is the point.
	_ = exec.CommandContext(ctx, "sh", "-c", usecase.WorkspaceRemoveDirScript(ws)).Run() // #nosec G204 -- every path in the script is shellx-quoted
	_, err := os.Lstat(ws)
	return errors.Is(err, os.ErrNotExist)
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
