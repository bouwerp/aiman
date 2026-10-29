package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/bouwerp/aiman/internal/coordinator"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/local"
	"github.com/bouwerp/aiman/internal/usecase"
)

// consultEndedReason is the session.ended reason RetireConsult records.
const consultEndedReason = "consult_finished"

// SetConsultDir sets the directory consult scratch directories are made in.
// Empty is ~/.aiman/consults.
func (s *Server) SetConsultDir(dir string) {
	s.consultDir = dir
}

// consultBase is the directory holding every consult's scratch directory.
func (s *Server) consultBase() string {
	if s.consultDir != "" {
		return s.consultDir
	}
	home := hostHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, config.DirName, "consults")
}

// handleConsult validates a consult create and resolves the parent and the
// item it answers for. It runs before applyFor, so an open design gate does
// not block it: a consult is part of reaching approval.
func (s *Server) handleConsult(ctx context.Context, req Request, p createParams, parentID string, existing []domain.Session) Response {
	tier := strings.ToLower(strings.TrimSpace(p.Tier))
	switch {
	case tier != "" && tier != config.TierSmart:
		return errResp(req.ID, CodeInvalidParams, "a consult runs on the smart tier only")
	case strings.TrimSpace(p.Agent) != "" || strings.TrimSpace(p.Model) != "" || strings.TrimSpace(p.Effort) != "":
		return errResp(req.ID, CodeInvalidParams, "a consult picks its agent from agents.smart; drop agent, model and effort")
	case strings.TrimSpace(p.For) == "":
		return errResp(req.ID, CodeInvalidParams, "consult needs for: the to-do the consult answers")
	case parentID == "":
		return errResp(req.ID, CodeInvalidParams, "consult needs a parent session (--parent, or run inside one)")
	}
	parent, ok := resolveSession(existing, parentID)
	if !ok {
		return errResp(req.ID, CodeNotFound, fmt.Sprintf("parent session %s not found", parentID))
	}
	list, err := s.TodoAccess().Get(parent.ID)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	i, err := findTodo(&list, strings.TrimSpace(p.For))
	if err != nil {
		return errResp(req.ID, CodeNotFound, fmt.Sprintf("%v in session %s", err, parent.Name))
	}
	item := list.Items[i]
	if item.Ref != "" {
		return errResp(req.ID, CodeInvalidParams, fmt.Sprintf("to-do %s is already linked to %s", item.ID, item.Ref))
	}
	brief, err := readBrief(p.Brief, p.BriefFile, true)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	if brief == "" {
		return errResp(req.ID, CodeInvalidParams, "a consult needs a brief: ## Question, ## Scope, ## Report must contain")
	}
	p.Brief, p.BriefFile = brief, ""
	return s.createConsult(ctx, req, p, parent, item)
}

// createConsult starts a hidden smart-tier consult for item in a fresh
// scratch repository, writes p.Brief (already canonical) as its task file and
// links item to it. The consult reads the parent's worktree and never gets a
// branch of the parent's repository.
func (s *Server) createConsult(ctx context.Context, req Request, p createParams, parent domain.Session, item domain.TodoItem) Response {
	if s.create == nil {
		return errResp(req.ID, CodeCreateFailed, "create not configured")
	}
	pick, err := s.pickConsult(ctx, parent)
	if err != nil {
		return errResp(req.ID, CodeConsultNeedsSmart, err.Error())
	}
	base := s.consultBase()
	if base == "" {
		return errResp(req.ID, CodeCreateFailed, "no home directory for consult scratch directories")
	}
	id := uuid.NewString()[:8]
	dir := filepath.Join(base, id)
	if err := prepareConsultDir(ctx, dir, p.Brief); err != nil {
		_ = os.RemoveAll(dir)
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	sess, err := s.startConsult(ctx, consultConfig(parent, pick, dir, id))
	if err != nil {
		_ = os.RemoveAll(dir)
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	if err := s.linkChild(ctx, parent.ID, item.ID, sess.ID); err != nil {
		s.RetireConsult(ctx, sess)
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	s.appendEvent(ctx, sess.ID, domain.EventActorAiman, domain.EventSessionCreated, map[string]any{
		"name": sess.Name, "group": sess.Group, "role": string(domain.SessionRoleConsult), "parent": parent.ID,
		"agent": pick.Agent, "tier": config.TierSmart, "requested_tier": config.TierSmart,
		"model": pick.Model, "effort": pick.Effort, "tier_fallback": false,
	})
	return Response{ID: req.ID, Result: map[string]any{
		"type": "session", "session": sessionInfo(sess, req.Caller),
		"tier_fallback": false, "requested_tier": config.TierSmart,
	}}
}

// pickConsult chooses the consult's agent from this host's installed smart
// tier, away from the parent's own agent and model where it can.
func (s *Server) pickConsult(ctx context.Context, parent domain.Session) (config.PreferredAgent, error) {
	installed := func(command string) bool {
		return s.agentInstalled != nil && s.agentInstalled(ctx, command)
	}
	model := parent.LaunchModel
	if model == "" {
		model = parent.AgentModel
	}
	return coordinator.PickConsult(s.agents.Smart, parent.AgentName, model, installed)
}

// prepareConsultDir makes dir a fresh git repository holding the brief as its
// task file.
func prepareConsultDir(ctx context.Context, dir, brief string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", dir, "init", "-q").CombinedOutput(); err != nil { //nolint:gosec // dir is a consult scratch directory under consultBase
		return fmt.Errorf("git init %s: %w: %s", dir, err, strings.TrimSpace(string(out)))
	}
	return usecase.WriteTaskBrief(ctx, local.NewExecutor(dir), dir, brief)
}

// consultConfig is the create config of a consult in dir for parent.
func consultConfig(parent domain.Session, pick config.PreferredAgent, dir, id string) domain.SessionConfig {
	childAgent := domain.Agent{Name: pick.Agent, Command: pick.Agent, LaunchModel: pick.Model, LaunchEffort: pick.Effort}
	if known, ok := agent.FindKnown(pick.Agent); ok {
		known.LaunchModel, known.LaunchEffort = pick.Model, pick.Effort
		childAgent = known
	}
	worktree := parent.WorktreePath
	if worktree == "" {
		worktree = parent.WorkingDirectory
	}
	name := "consult-" + id
	if parent.Name != "" {
		name = parent.Name + "-" + name
	}
	return domain.SessionConfig{
		Name:             name,
		Group:            parent.Group,
		ParentID:         parent.ID,
		Role:             domain.SessionRoleConsult,
		Tier:             config.TierSmart,
		Agent:            &childAgent,
		AdHoc:            true,
		PromptFree:       true,
		PreparedWorktree: dir,
		RemoteHost:       parent.RemoteHost,
		InitialPrompt: "Read " + domain.AimanTaskFileName + " and follow your brief. The parent worktree is " +
			worktree + "; read it, do not edit it. Write your report to " +
			filepath.Join(dir, domain.AimanReportFileName) + ", never into the parent worktree.",
	}
}

// startConsult creates the consult and stores its row with the identity
// fields the creator does not set.
func (s *Server) startConsult(ctx context.Context, cfg domain.SessionConfig) (domain.Session, error) {
	created, err := s.create.CreateSession(ctx, cfg)
	if err != nil {
		return domain.Session{}, err
	}
	sess := *created
	sess.Name, sess.Group, sess.ParentID, sess.Role, sess.Tier = cfg.Name, cfg.Group, cfg.ParentID, cfg.Role, cfg.Tier
	// The scratch directory is the consult's whole world: the tracker reads
	// its report there and RetireConsult removes it.
	sess.WorktreePath, sess.WorkingDirectory = cfg.PreparedWorktree, cfg.PreparedWorktree
	if sess.LaunchModel == "" {
		sess.LaunchModel = cfg.Agent.LaunchModel
	}
	if sess.LaunchEffort == "" {
		sess.LaunchEffort = cfg.Agent.LaunchEffort
	}
	if s.repo != nil {
		if err := s.repo.Save(ctx, &sess); err != nil {
			s.RetireConsult(ctx, sess)
			return domain.Session{}, err
		}
	}
	return sess, nil
}

// RetireConsult ends a consult: it stops its terminal, forgets its row and
// removes its scratch directory when that lies directly under the consult
// base. Failures are logged; a retire is best effort.
func (s *Server) RetireConsult(ctx context.Context, child domain.Session) {
	if err := (serverTerminal{s}).Stop(ctx, child); err != nil {
		log.Printf("consult: stop %s: %v", child.ID, err)
	}
	if err := s.dropSession(ctx, child); err != nil {
		log.Printf("consult: forget %s: %v", child.ID, err)
	}
	s.appendEvent(ctx, child.ID, domain.EventActorAiman, domain.EventSessionEnded, map[string]any{"reason": consultEndedReason})
	dir := filepath.Clean(child.WorkingDirectory)
	base := s.consultBase()
	if child.WorkingDirectory == "" || base == "" || filepath.Dir(dir) != filepath.Clean(base) {
		return
	}
	if err := os.RemoveAll(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("consult: remove %s: %v", dir, err)
	}
}

// KeepConsultReport copies a consult's report to <consult base>/<id>.report.md,
// beside the scratch directory RetireConsult removes, and returns its path.
func (s *Server) KeepConsultReport(_ context.Context, child domain.Session, md string) (string, error) {
	base := s.consultBase()
	if base == "" {
		return "", errors.New("no home directory for consult reports")
	}
	if child.ID == "" || child.ID != filepath.Base(child.ID) || strings.ContainsAny(child.ID, `/\`) || child.ID == ".." {
		return "", fmt.Errorf("consult id %q is not a file name", child.ID)
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(base, child.ID+".report.md")
	if err := os.WriteFile(path, []byte(md), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// ConsultAlive reports whether a consult's terminal still runs in serve's
// PTY runtime.
func (s *Server) ConsultAlive(ctx context.Context, child domain.Session) bool {
	return serverTerminal{s}.Alive(ctx, child)
}

// AppendLedger writes one session_events row with actor aiman. It never fails.
func (s *Server) AppendLedger(ctx context.Context, sessionID string, kind domain.EventKind, payload map[string]any) {
	s.appendEvent(ctx, sessionID, domain.EventActorAiman, kind, payload)
}
