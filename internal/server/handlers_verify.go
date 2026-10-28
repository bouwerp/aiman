package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/notify"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/verify"
)

// SetVerifier attaches the review manager. Must be called before Serve;
// without it verify.start and verify.status fail.
func (s *Server) SetVerifier(v *verify.Manager) {
	s.verifier = v
}

// VerifyDeps returns the manager's collaborators backed by this server: its
// sessions, terminals, to-do lists, context store and notifications.
func (s *Server) VerifyDeps(store *verify.Store, installed func(string) bool) verify.Deps {
	return verify.Deps{
		Store:     store,
		Sessions:  serverSessions{s: s, cleanupReason: "review_cleanup"},
		Terminal:  serverTerminal{s},
		Todos:     s.TodoAccess(),
		Context:   s.ctxStore,
		Notifier:  serveNotifier{s},
		Installed: installed,
	}
}

func (s *Server) handleVerifyStart(ctx context.Context, req Request) Response {
	if s.verifier == nil {
		return errResp(req.ID, CodeInvalidParams, "verify not available")
	}
	var p struct {
		Session   string `json:"session"`
		Agent     string `json:"agent"`
		IfEnabled bool   `json:"if_enabled"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	target := p.Session
	if strings.TrimSpace(target) == "" {
		target = req.Caller
	}
	if strings.TrimSpace(target) == "" {
		return errResp(req.ID, CodeInvalidParams, "session is required")
	}
	sess, ok := s.findSession(ctx, target)
	if !ok {
		return errResp(req.ID, CodeNotFound, "session not found")
	}
	r, err := s.verifier.Start(ctx, verify.StartRequest{WorkerID: sess.ID, Agent: p.Agent, IfEnabled: p.IfEnabled})
	switch {
	case errors.Is(err, verify.ErrNoReviewer):
		return errResp(req.ID, CodeNoReviewer, err.Error())
	case err != nil:
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	return reviewResponse(req.ID, r)
}

func (s *Server) handleVerifyStatus(ctx context.Context, req Request) Response {
	if s.verifier == nil {
		return errResp(req.ID, CodeInvalidParams, "verify not available")
	}
	var p struct {
		Review   string `json:"review"`
		Session  string `json:"session"`
		TreeHash string `json:"tree_hash"`
		// Gate marks the before-PR gate asking; a denial is recorded in
		// the session's ledger so a blocked PR is auditable.
		Gate bool `json:"gate"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	if p.Review != "" {
		r, err := s.verifier.Status(p.Review)
		if errors.Is(err, verify.ErrNotFound) {
			return errResp(req.ID, CodeNotFound, "review not found")
		}
		if err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
		return reviewResponse(req.ID, r)
	}
	target := p.Session
	if strings.TrimSpace(target) == "" {
		target = req.Caller
	}
	sess, ok := s.findSession(ctx, target)
	if !ok {
		return errResp(req.ID, CodeNotFound, "session not found")
	}
	if p.TreeHash != "" {
		return s.verifyStatusForTree(ctx, req.ID, sess, p.TreeHash, p.Gate)
	}
	r, ok := s.verifier.LatestFor(sess.ID)
	if !ok {
		return errResp(req.ID, CodeNotFound, "no review for this session")
	}
	return reviewResponse(req.ID, r)
}

// verifyStatusForTree answers the before-PR gate: a disabled review when
// before_pr is off, else the latest review of exactly this tree. When the
// gate asks and the answer is not a pass, the denial goes to the ledger.
func (s *Server) verifyStatusForTree(ctx context.Context, id string, sess domain.Session, tree string, gate bool) Response {
	if !s.verifier.BeforePR() {
		return reviewResponse(id, verify.Review{WorkerID: sess.ID, Tree: tree, Status: verify.StatusDisabled})
	}
	r, ok := s.verifier.ForTree(sess.ID, tree)
	if gate && (!ok || !r.Passed()) {
		payload := map[string]any{"kind": "gate", "tree": tree, "reason": "no review of this tree"}
		if ok {
			payload["review"] = r.ID
			payload["status"] = string(r.Status)
			payload["required"] = r.Required
			payload["reason"] = "review did not pass"
		}
		s.appendEvent(ctx, sess.ID, domain.EventActorVerifier, domain.EventVerifyRecorded, payload)
	}
	if !ok {
		return errResp(id, CodeNotFound, "no review of this tree")
	}
	return reviewResponse(id, r)
}

func reviewResponse(id string, r verify.Review) Response {
	return Response{ID: id, Result: map[string]any{"type": "review", "review": r}}
}

// findSession resolves a name, id or terminal name among serve's sessions.
func (s *Server) findSession(ctx context.Context, target string) (domain.Session, bool) {
	list, err := s.loadSessions(ctx)
	if err != nil {
		return domain.Session{}, false
	}
	return resolveSession(list, target)
}

// serverSessions is a child manager's session registry. cleanupReason is the
// session-ended reason Forget records for the children that manager drops.
type serverSessions struct {
	s             *Server
	cleanupReason string
}

// Get wraps domain.ErrSessionNotFound only when the registry was read and
// has no such session; a failed read is a different error.
func (a serverSessions) Get(ctx context.Context, id string) (domain.Session, error) {
	list, err := a.s.loadSessions(ctx)
	if err != nil {
		return domain.Session{}, fmt.Errorf("reading sessions for %s: %w", id, err)
	}
	sess, ok := resolveSession(list, id)
	if !ok {
		return domain.Session{}, fmt.Errorf("session %s: %w", id, domain.ErrSessionNotFound)
	}
	return a.s.liveSession(ctx, sess), nil
}

// Children lists the sessions whose parent is parentID.
func (a serverSessions) Children(ctx context.Context, parentID string) ([]domain.Session, error) {
	list, err := a.s.loadSessions(ctx)
	if err != nil {
		return nil, err
	}
	var out []domain.Session
	for _, sess := range list {
		if sess.ParentID == parentID {
			out = append(out, sess)
		}
	}
	return out, nil
}

// CreateChild creates the child through serve's creator and stores the
// identity fields the creator does not set.
func (a serverSessions) CreateChild(ctx context.Context, cfg domain.SessionConfig) (domain.Session, error) {
	if a.s.create == nil {
		return domain.Session{}, errors.New("session create is not available")
	}
	sess, err := a.s.create.CreateSession(ctx, cfg)
	if err != nil {
		return domain.Session{}, err
	}
	sess.Name, sess.Group, sess.ParentID, sess.Role = cfg.Name, cfg.Group, cfg.ParentID, cfg.Role
	if a.s.repo != nil {
		if err := a.s.repo.Save(ctx, sess); err != nil {
			return domain.Session{}, err
		}
	}
	a.s.appendEvent(ctx, sess.ID, domain.EventActorAiman, domain.EventSessionCreated, map[string]any{
		"name": cfg.Name, "group": cfg.Group, "role": string(cfg.Role), "parent": cfg.ParentID,
	})
	return *sess, nil
}

func (a serverSessions) Forget(ctx context.Context, id string) error {
	sess, ok := a.s.findSession(ctx, id)
	if !ok {
		return nil // already gone
	}
	if err := a.s.dropSession(ctx, sess); err != nil {
		return err
	}
	a.s.appendEvent(ctx, sess.ID, domain.EventActorAiman, domain.EventSessionEnded, map[string]any{"reason": a.cleanupReason})
	return nil
}

// serverTerminal checks and stops a session's process in serve's PTY runtime.
type serverTerminal struct{ s *Server }

func (a serverTerminal) Alive(_ context.Context, sess domain.Session) bool {
	if a.s.pty == nil || sess.IsLegacyTmux() {
		return false
	}
	info, err := a.s.pty.Get(sess.ID)
	return err == nil && info.Status == ptyruntime.StatusRunning
}

// Stop kills the session's PTY. A legacy tmux session is refused: aiman
// never runs tmux, so the user stops it on the host.
func (a serverTerminal) Stop(_ context.Context, sess domain.Session) error {
	if sess.IsLegacyTmux() {
		return domain.NewLegacyTmuxError(sess)
	}
	if a.s.pty == nil {
		return nil
	}
	return a.s.pty.Kill(sess.ID)
}

// quoteArg single-quotes s for a POSIX shell.
func quoteArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// serveNotifier publishes every finished review on the hub and raises
// review_done only when there is something for the user to act on.
type serveNotifier struct{ s *Server }

func (n serveNotifier) ReviewFinished(ctx context.Context, w domain.Session, r verify.Review) {
	n.s.HubPublish(SessionEvent{Type: "verify", ID: w.ID, State: string(r.Status)})
	if r.Required == 0 {
		log.Printf("verify: %s %s, no required findings", w.ID, r.Status)
		return
	}
	detail := fmt.Sprintf("%d required, %d suggestion(s)", r.Required, r.Suggestions)
	if err := n.s.NotifyRecorder().Emit(ctx, w.ID, notify.Label(w.IssueKey, w.Name, w.ID), domain.NotifyReviewDone, detail); err != nil {
		log.Printf("verify: notify %s: %v", w.ID, err)
	}
}
