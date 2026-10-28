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
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/contextstore"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/fsutil"
	"github.com/bouwerp/aiman/internal/handover"
	"github.com/bouwerp/aiman/internal/handoverchild"
	"github.com/bouwerp/aiman/internal/memoryjob"
	"github.com/bouwerp/aiman/internal/pane"
)

// HandoverRestartNote is the steer text queued when a handover file is already
// on disk after a terminal restart. The agent reads that file instead of a
// pasted copy of it.
const HandoverRestartNote = "[aiman] This session's terminal was restarted. Your last handover is in .aiman_handover.md; read it if you lost track."

// HandoverCompactionNote is the same pointer after a PreCompact handover.
const HandoverCompactionNote = "[aiman] Your context was compacted. Your handover is in .aiman_handover.md; read it if you lost track."

// handoverResult is one written handover. Written is false when the file
// bytes were already current. Child is the handover child run that started or
// was already active; it is empty when no child runs.
type handoverResult struct {
	Path    string `json:"path"`
	Written bool   `json:"written"` // file content changed
	NoteID  string `json:"note_id"`
	Child   string `json:"child,omitempty"`
}

// SetHandovers attaches the handover child manager. Call it before Serve and
// before the manager's Resume.
func (s *Server) SetHandovers(m *handoverchild.Manager) {
	s.handovers = m
}

// HandoverDeps returns the handover child manager's collaborators backed by
// this server. The caller sets Candidates, BaseDir and Timeout.
func (s *Server) HandoverDeps(store *handoverchild.Store, installed func(string) bool) handoverchild.Deps {
	d := handoverchild.Deps{
		Store:     store,
		Sessions:  serverSessions{s: s, cleanupReason: "child_cleanup"},
		Terminal:  serverTerminal{s},
		Steer:     serveSteerAiman{s},
		Ledger:    serveLedger{s},
		Installed: installed,
	}
	// A typed nil would pass the manager's nil checks and then panic.
	if s.ctxStore != nil {
		d.Context = s.ctxStore
	}
	if s.memoryJobs != nil {
		d.Jobs = s.memoryJobs
	}
	return d
}

// serveSteerAiman queues a note with author aiman on serve's steerer.
type serveSteerAiman struct{ s *Server }

func (a serveSteerAiman) SteerAsAiman(sessionID, text string) {
	if a.s.steer == nil {
		return
	}
	a.s.steer.Enqueue(SteerNote{SessionID: sessionID, Author: domain.AuthorAiman, Text: text})
}

// serveLedger appends session_events rows with actor aiman.
type serveLedger struct{ s *Server }

func (a serveLedger) Append(ctx context.Context, sessionID string, kind domain.EventKind, payload map[string]any) {
	a.s.appendEvent(ctx, sessionID, domain.EventActorAiman, kind, payload)
}

func handoverNoteID(sessionID string) string {
	return "handover-" + contextstore.SafeKey(sessionID)
}

// handoverCall is the params object for handover.write and handover.point.
type handoverCall struct {
	Session string `json:"session"`
	Reason  string `json:"reason"`
}

func decodeHandoverCall(req Request) (handoverCall, *Response) {
	var p handoverCall
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			r := errResp(req.ID, CodeInvalidParams, "invalid params")
			return p, &r
		}
	}
	return p, nil
}

func validHandoverReason(reason string) bool {
	switch reason {
	case "archive", "forget", "restart", "agent_switch", "pre_compact":
		return true
	default:
		return false
	}
}

// lookupHandoverSession resolves session the way todo.* does: id, name, or
// group/name. A miss returns not_found without starting handover work.
func (s *Server) lookupHandoverSession(ctx context.Context, id, name string) (domain.Session, *Response) {
	if strings.TrimSpace(name) == "" {
		r := errResp(id, CodeInvalidParams, "session is required")
		return domain.Session{}, &r
	}
	list, err := s.loadSessions(ctx)
	if err != nil {
		r := errResp(id, CodeInvalidParams, err.Error())
		return domain.Session{}, &r
	}
	sess, ok := resolveSession(list, name)
	if !ok {
		r := errResp(id, CodeNotFound, "session not found")
		return domain.Session{}, &r
	}
	return sess, nil
}

func (s *Server) handleHandoverWrite(ctx context.Context, req Request) Response {
	p, bad := decodeHandoverCall(req)
	if bad != nil {
		return *bad
	}
	if !validHandoverReason(p.Reason) {
		return errResp(req.ID, CodeInvalidParams, "reason must be one of archive, forget, restart, agent_switch, pre_compact")
	}
	sess, bad := s.lookupHandoverSession(ctx, req.ID, p.Session)
	if bad != nil {
		return *bad
	}
	// 20s stays under the 30s socket deadline. The slow work is pane capture
	// and git diff, and an unknown session has already returned.
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	res, err := s.writeHandover(ctx, sess, p.Reason)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	result := map[string]any{
		"type":    "handover",
		"path":    res.Path,
		"written": res.Written,
		"note_id": res.NoteID,
		"exists":  res.Path != "",
	}
	if res.Child != "" {
		result["child"] = res.Child
	}
	return Response{ID: req.ID, Result: result}
}

func (s *Server) handleHandoverPoint(ctx context.Context, req Request) Response {
	p, bad := decodeHandoverCall(req)
	if bad != nil {
		return *bad
	}
	if p.Reason != "" && !validHandoverReason(p.Reason) {
		return errResp(req.ID, CodeInvalidParams, "reason must be one of archive, forget, restart, agent_switch, pre_compact")
	}
	sess, bad := s.lookupHandoverSession(ctx, req.ID, p.Session)
	if bad != nil {
		return *bad
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return Response{ID: req.ID, Result: map[string]any{
		"type":   "handover_point",
		"queued": s.queueHandoverPointer(ctx, sess, p.Reason),
	}}
}

// queueHandoverPointer enqueues the restart or compaction pointer only when the
// handover file is already in the session directory. No directory, no file, no
// steerer, or an archive/forget reason queues nothing. While a handover child
// runs for the session, the pointer is held on the run and queued when it ends.
func (s *Server) queueHandoverPointer(ctx context.Context, sess domain.Session, reason string) bool {
	if ctx.Err() != nil || s.steer == nil || !handoverFileExists(sess) {
		return false
	}
	text := HandoverRestartNote
	switch reason {
	case "pre_compact":
		text = HandoverCompactionNote
	case "", "restart", "agent_switch":
	default:
		return false
	}
	if s.handovers != nil && s.handovers.HoldPointer(sess.ID, text) {
		return true
	}
	s.steer.Enqueue(SteerNote{
		SessionID: sess.ID,
		Author:    domain.AuthorAiman,
		Text:      text,
	})
	return true
}

func handoverFileExists(sess domain.Session) bool {
	dir, err := handoverDir(sess)
	if err != nil {
		return false
	}
	return regularFileExists(filepath.Join(dir, domain.AimanHandoverFileName))
}

func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// preCompactHandoverTimeout bounds the synchronous PreCompact handover.
// Production stays at 20s, under the 30s socket cap. Tests shorten it.
var preCompactHandoverTimeout = 20 * time.Second

// notePreCompactHandover writes the handover before the report returns and
// queues a compaction pointer. The 20s budget stays under the 30s socket cap.
// A wedged diff is cancelled and becomes an empty stat. A write error is
// logged and the report still succeeds; a failed write queues no pointer.
func (s *Server) notePreCompactHandover(ctx context.Context, sess domain.Session) {
	ctx, cancel := context.WithTimeout(ctx, preCompactHandoverTimeout)
	defer cancel()
	if _, err := s.writeHandover(ctx, sess, "pre_compact"); err != nil {
		log.Printf("handover: pre_compact %s: %v", sess.ID, err)
		return
	}
	s.queueHandoverPointer(ctx, sess, "pre_compact")
}

// noteForgetHandover writes before the terminal is killed. The 10s budget
// stops a stuck diff from holding forget, and a failure still leaves the
// row to be deleted.
func (s *Server) noteForgetHandover(ctx context.Context, sess domain.Session) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := s.writeHandover(ctx, sess, "forget"); err != nil {
		log.Printf("handover: forget %s: %v", sess.ID, err)
	}
}

// writeHandover gathers the session's pane, list, undelivered notes, and diff
// stat, then writes .aiman_handover.md. The context note is updated when the
// file changed or the note is not stored yet. A note-put failure is logged;
// the file write is the result the caller gets. Hidden sessions get no
// handover.
//
// With a handover child manager, every write also starts a child (or joins
// the active one), and the run enqueues the memory job and stores the final
// note when it ends. A render whose sha256 matches the run that last rewrote
// the file leaves that rewrite in place and starts nothing.
func (s *Server) writeHandover(ctx context.Context, sess domain.Session, reason string) (handoverResult, error) {
	if sess.Role.Hidden() {
		return handoverResult{}, nil
	}
	dir, err := handoverDir(sess)
	if err != nil {
		return handoverResult{}, err
	}
	in := s.handoverInput(ctx, sess, reason, dir)
	rendered := []byte(handover.Render(in))
	sum := sha256.Sum256(rendered)
	sha := hex.EncodeToString(sum[:])
	path := filepath.Join(dir, domain.AimanHandoverFileName)
	id := handoverNoteID(sess.ID)
	if s.childRewroteSameState(sess.ID, sha, path) {
		s.appendEvent(ctx, sess.ID, domain.EventActorAiman, domain.EventHandoverWritten, map[string]any{
			"path": path, "written": false, "reason": reason, "kept": "child",
		})
		return handoverResult{Path: path, NoteID: id}, nil
	}
	changed, err := fsutil.WriteIfChanged(path, rendered, 0o644)
	if err != nil {
		return handoverResult{}, fmt.Errorf("writing handover: %w", err)
	}
	s.putHandoverNote(ctx, sess, in, id, changed)
	s.appendEvent(ctx, sess.ID, domain.EventActorAiman, domain.EventHandoverWritten, map[string]any{
		"path": path, "written": changed, "reason": reason,
	})
	child := s.startHandoverChild(ctx, sess, reason, in, rendered, sha)
	if child == "" && changed && s.memoryJobs != nil {
		if err := s.memoryJobs.Enqueue(ctx, memoryjob.Job{
			SessionID: sess.ID,
			Reason:    reason,
			Group:     sess.Group,
			Repo:      sess.RepoName,
			Label:     in.Label,
			Source:    handover.NoteBody(in),
		}); err != nil {
			log.Printf("memory job: %s: %v", sess.ID, err)
		}
	}
	return handoverResult{Path: path, Written: changed, NoteID: id, Child: child}, nil
}

// childRewroteSameState reports whether the file on disk is, byte for byte,
// a child's rewrite of a render identical to sha.
func (s *Server) childRewroteSameState(sessionID, sha, path string) bool {
	if s.handovers == nil {
		return false
	}
	mech, applied := s.handovers.LastApplied(sessionID)
	if mech != sha || applied == "" {
		return false
	}
	body, err := os.ReadFile(path) //nolint:gosec // path is the session's handover file
	if err != nil {
		return false
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]) == applied
}

// startHandoverChild starts a handover child, or joins the active one, and
// returns its run id. A refused start returns "" so the caller enqueues the
// memory job itself.
func (s *Server) startHandoverChild(ctx context.Context, sess domain.Session, reason string, in handover.Input, rendered []byte, sha string) string {
	if s.handovers == nil {
		return ""
	}
	run, _, err := s.handovers.Start(ctx, handoverchild.StartRequest{
		Worker:        sess,
		Reason:        reason,
		Mechanical:    rendered,
		MechanicalSHA: sha,
		Label:         in.Label,
		Abstract:      handover.Abstract(in),
	})
	if err != nil {
		log.Printf("handover: child for %s: %v", sess.ID, err)
		return ""
	}
	return run
}

func handoverDir(sess domain.Session) (string, error) {
	dir := strings.TrimSpace(sess.WorkingDirectory)
	if dir == "" {
		dir = strings.TrimSpace(sess.WorktreePath)
	}
	if dir == "" {
		return "", errors.New("session has no working directory")
	}
	return dir, nil
}

func (s *Server) handoverInput(ctx context.Context, sess domain.Session, reason, dir string) handover.Input {
	base := strings.TrimSpace(sess.BaseBranch)
	// The pane read is local and short. Git shares this deadline, so a diff
	// that runs the context out must not cancel the capture. An empty stat
	// is still a successful write.
	pane := s.handoverPane(ctx, sess)
	stat := ""
	if s.remote != nil {
		stat = diffStat(ctx, s.remote, dir, base)
	}
	return handover.Input{
		Label:       handoverLabel(sess),
		Reason:      reason,
		Agent:       sess.AgentName,
		Branch:      sess.Branch,
		BaseBranch:  handoverBaseLabel(base),
		Todos:       s.handoverTodos(sess.ID),
		Undelivered: s.handoverUndelivered(sess.ID),
		DiffStat:    stat,
		HookTitle:   sess.AgentTitle,
		HookMessage: sess.HookStateMessage,
		Blocked:     handoverBlocked(sess),
		PaneTail:    pane,
	}
}

// handoverBlocked is the agent's own waiting reason, when the hook reported
// one. It is context for the next agent, not a health verdict.
func handoverBlocked(sess domain.Session) string {
	switch sess.HookState {
	case domain.AgentStateWaitingInput, domain.AgentStateWaitingBackground:
		if msg := strings.TrimSpace(sess.HookStateMessage); msg != "" {
			return msg
		}
		return string(sess.HookState)
	default:
		return ""
	}
}

func handoverLabel(sess domain.Session) string {
	for _, part := range []string{sess.Name, sess.IssueKey, sess.ID} {
		if part = strings.TrimSpace(part); part != "" {
			return part
		}
	}
	return ""
}

// handoverBaseLabel is what the file shows. An empty session base is rendered
// as origin/HEAD; the diff command still receives the empty base so git
// resolves that ref itself, and a miss stays an empty stat.
func handoverBaseLabel(base string) string {
	if base == "" {
		return "origin/HEAD"
	}
	return base
}

func (s *Server) handoverTodos(id string) *domain.TodoList {
	if s.todos == nil {
		return nil
	}
	list, err := s.todos.Get(id)
	if err != nil {
		log.Printf("handover: todos %s: %v", id, err)
		return nil
	}
	// A session with no list comes back as Rev 0 and a nil error. That is not
	// a list. A real list has Rev > 0 even when it currently has no items.
	if list.Rev == 0 {
		return nil
	}
	return &list
}

func (s *Server) handoverUndelivered(id string) []string {
	if s.steer == nil {
		return nil
	}
	pending := s.steer.Status(id).Pending
	if len(pending) == 0 {
		return nil
	}
	out := make([]string, len(pending))
	for i, n := range pending {
		out[i] = n.Text
	}
	return out
}

func (s *Server) handoverPane(ctx context.Context, sess domain.Session) string {
	raw, err := s.captureHandoverPane(ctx, sess)
	if err != nil {
		return ""
	}
	return paneTail(raw)
}

func (s *Server) captureHandoverPane(ctx context.Context, sess domain.Session) (string, error) {
	if sess.IsLegacyTmux() || s.pty == nil {
		return "", nil
	}
	return s.pty.CaptureScreen(sess.ID)
}

// paneTail keeps the last handover.PaneLines of a cleaned capture. Trailing
// blank lines from the capture are dropped before the window is taken.
func paneTail(raw string) string {
	cleaned := strings.TrimRight(pane.Clean(raw), "\n")
	if cleaned == "" {
		return ""
	}
	lines := strings.Split(cleaned, "\n")
	if len(lines) > handover.PaneLines {
		lines = lines[len(lines)-handover.PaneLines:]
	}
	return strings.Join(lines, "\n")
}

func (s *Server) putHandoverNote(ctx context.Context, sess domain.Session, in handover.Input, id string, changed bool) {
	if s.ctxStore == nil {
		return
	}
	if !changed {
		if _, err := s.ctxStore.Get(ctx, id); err == nil {
			return
		}
	}
	ns, key := handover.NoteNamespace(sess.Group, sess.IssueKey, sess.RepoName)
	_, err := s.ctxStore.Put(ctx, domain.ContextEntry{
		ID:        id,
		Namespace: ns,
		Key:       key,
		Title:     "Handover: " + in.Label,
		Abstract:  handover.Abstract(in),
		Body:      handover.NoteBody(in),
		SessionID: sess.ID,
		Type:      domain.ContextTypeHandover,
		Author:    domain.ContextAuthorAiman,
	})
	if err != nil {
		log.Printf("handover: context note %s: %v", id, err)
	}
}

// diffStat is `git diff --stat` against the merge base of base and HEAD.
// A directory that is not a git repo, or a base git cannot resolve, is an
// empty string. The renderer shows that as "(unavailable)".
func diffStat(ctx context.Context, ex interface {
	Execute(context.Context, string) (string, error)
}, dir, base string) string {
	if ex == nil {
		return ""
	}
	cmd := fmt.Sprintf("cd %s && git rev-parse --git-dir >/dev/null 2>&1 || exit 0\n"+
		"base=%s; [ -n \"$base\" ] || base=$(git rev-parse --abbrev-ref origin/HEAD 2>/dev/null) || exit 0\n"+
		"mb=$(git merge-base \"$base\" HEAD 2>/dev/null) || exit 0\n"+
		"git diff --stat \"$mb\"",
		strconv.Quote(dir), strconv.Quote(base))
	out, err := ex.Execute(ctx, cmd)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
