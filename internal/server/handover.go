package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bouwerp/aiman/internal/contextstore"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/fsutil"
	"github.com/bouwerp/aiman/internal/handover"
	"github.com/bouwerp/aiman/internal/pane"
)

// handoverResult is one written handover. Written is false when the file
// bytes were already current.
type handoverResult struct {
	Path    string `json:"path"`
	Written bool   `json:"written"` // file content changed
	NoteID  string `json:"note_id"`
}

func handoverNoteID(sessionID string) string {
	return "handover-" + contextstore.SafeKey(sessionID)
}

// writeHandover gathers the session's pane, list, undelivered notes, and diff
// stat, then writes .aiman_handover.md. The context note is updated when the
// file changed or the note is not stored yet. A note-put failure is logged;
// the file write is the result the caller gets.
func (s *Server) writeHandover(ctx context.Context, sess domain.Session, reason string) (handoverResult, error) {
	dir, err := handoverDir(sess)
	if err != nil {
		return handoverResult{}, err
	}
	in := s.handoverInput(ctx, sess, reason, dir)
	path := filepath.Join(dir, domain.AimanHandoverFileName)
	changed, err := fsutil.WriteIfChanged(path, []byte(handover.Render(in)), 0o644)
	if err != nil {
		return handoverResult{}, fmt.Errorf("writing handover: %w", err)
	}
	id := handoverNoteID(sess.ID)
	s.putHandoverNote(ctx, sess, in, id, changed)
	return handoverResult{Path: path, Written: changed, NoteID: id}, nil
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
		PaneTail:    s.handoverPane(ctx, sess),
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
	if sess.IsPTY() && s.pty != nil {
		return s.pty.CaptureScreen(sess.ID)
	}
	if s.remote == nil {
		return "", nil
	}
	return s.remote.CaptureTmuxPane(ctx, sess.TmuxSession)
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
	ns, key := handoverNamespace(sess)
	_, err := s.ctxStore.Put(ctx, domain.ContextEntry{
		ID:        id,
		Namespace: ns,
		Key:       key,
		Title:     "Handover: " + in.Label,
		Abstract:  handover.Abstract(in),
		Body:      handover.NoteBody(in),
		SessionID: sess.ID,
	})
	if err != nil {
		log.Printf("handover: context note %s: %v", id, err)
	}
}

// handoverNamespace matches contextstore.EntryFromSnapshot: group, else the
// issue key as a group, else the repo, else the host note.
func handoverNamespace(sess domain.Session) (string, string) {
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
