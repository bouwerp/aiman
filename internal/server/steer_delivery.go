package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/pane"
	"github.com/bouwerp/aiman/internal/usecase"
)

// Held reasons reported by steerReady.
const (
	steerHeldEnded   = "ended"
	steerHeldDialog  = "dialog"
	steerHeldWorking = "working"
	steerHeldUnknown = "unknown"
	// steerHeldLegacy holds notes for a session that still runs in tmux.
	// aiman no longer types into tmux, so the note waits for a move to pty.
	steerHeldLegacy = "legacy_terminal"
)

// steerClassifierQuiet is how long after a delivery the pane classifier must
// wait before an idle screen counts. The agent may not have redrawn yet, and
// the screen it shows is the one from before the note went in.
const steerClassifierQuiet = 3 * time.Second

// classifierQuietOpen reports whether the pane classifier may treat an idle
// screen as ready. lastDelivered is when Return was pressed. settled is when
// confirm finished; a note that arrived during confirm is held until the quiet
// window has passed from that later time. A zero anchor means nothing has been
// delivered yet.
func classifierQuietOpen(lastDelivered, settled, now time.Time) bool {
	anchor := lastDelivered
	if settled.After(anchor) {
		anchor = settled
	}
	if anchor.IsZero() {
		return true
	}
	return now.Sub(anchor) > steerClassifierQuiet
}

// steerSubmitTiming is the PTY submit budget: the remote prompt path's proven
// values, whose worst case (about 12 s) fits inside steerOpTimeout. A variable
// so tests can shorten it.
var steerSubmitTiming = usecase.DefaultSubmitTiming()

var _ steerTarget = (*Server)(nil)

// steerSession resolves id against the sessions this host knows.
func (s *Server) steerSession(ctx context.Context, id string) (domain.Session, bool) {
	sess := s.liveSessionByID(ctx, id)
	if sess == nil {
		return domain.Session{}, false
	}
	return *sess, true
}

// steerReady is the delivery gate. A note goes in when the agent waits at its
// prompt: a hook report of idle, or waiting_input raised by an idle prompt.
// Working and permission or choice dialogs hold it, as does an ended agent
// and an update dialog on a pty screen, whatever the hook last said.
// Sessions without a usable hook report fall back to the pane classifier, and
// only a high-confidence idle opens the gate.
//
// Readiness must be newer than lastDelivered: right after a note is typed the
// last report still says idle, and that must not let a second note in before
// the agent has picked up the first.
func (s *Server) steerReady(ctx context.Context, id string, lastDelivered time.Time) (bool, string) {
	sess, ok := s.steerSession(ctx, id)
	if !ok {
		return false, steerHeldUnknown
	}
	if sess.IsLegacyTmux() {
		return false, steerHeldLegacy
	}
	if sess.AgentEnded {
		return false, steerHeldEnded
	}
	if s.updateDialogShowing(sess) {
		return false, steerHeldDialog
	}
	if st, ok := agenthook.ResolveHookState(sess, time.Now()); ok {
		fresh := lastDelivered.IsZero() || sess.HookStateAt.After(lastDelivered)
		switch {
		case st == domain.AgentStateIdle,
			st == domain.AgentStateWaitingInput && sess.HookStateSource == agenthook.SourceIdlePrompt:
			if fresh {
				return true, ""
			}
			return false, steerHeldWorking
		case st == domain.AgentStateWaitingInput:
			return false, steerHeldDialog
		default:
			return false, steerHeldWorking
		}
	}
	if s.pty == nil {
		return false, steerHeldUnknown
	}
	st, conf := s.classify(ctx, sess)
	switch {
	case st == domain.AgentStateWaitingInput:
		return false, steerHeldDialog
	case st == domain.AgentStateIdle && conf == pane.High && classifierQuietOpen(lastDelivered, s.steerSettledAt(id), time.Now()):
		return true, ""
	default:
		return false, steerHeldUnknown
	}
}

// deliverSteer types text into the session's agent and submits it, and
// returns when it pressed Return. Serve owns the terminal, so the text is
// written through the PTY runtime in this process. A legacy tmux session is
// never typed into.
func (s *Server) deliverSteer(ctx context.Context, id, text string) (time.Time, error) {
	sess, ok := s.steerSession(ctx, id)
	if !ok {
		return time.Time{}, fmt.Errorf("steer: session %s not found", id)
	}
	if sess.IsLegacyTmux() {
		return time.Time{}, domain.NewLegacyTmuxError(sess)
	}
	text = steerOneLine(text)
	at, err := s.deliverPTYSteer(ctx, sess, text)
	return s.finishSteerSubmit(id, text, at, err)
}

// steerOneLine is the text actually typed. A newline submits on some agents,
// so the whole note is one line and the trailing Return is the only submit.
// Free text is not wrapped; this only removes the line breaks.
func steerOneLine(text string) string {
	text = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(text)
	var parts []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, " ")
}

func (s *Server) deliverPTYSteer(ctx context.Context, sess domain.Session, text string) (time.Time, error) {
	if s.pty == nil {
		return time.Time{}, errors.New("steer: pty runtime is not running")
	}
	write := func(b string) func(context.Context) error {
		return func(context.Context) error { return s.pty.Write(sess.ID, []byte(b)) }
	}
	return usecase.TypeAndSubmit(ctx, text, steerSubmitTiming, s.steerProgress(sess.ID, text),
		write(text), write("\r"),
		func(context.Context) (string, error) { return s.pty.CaptureScreen(sess.ID) })
}

// steerFlight is one session's unfinished type. text is the one-line payload.
type steerFlight struct {
	text     string
	typed    bool
	submitAt time.Time
}

func (s *Server) steerProgress(id, text string) usecase.SubmitProgress {
	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	f := s.steerFlights[id]
	if f.text != text {
		return usecase.SubmitProgress{}
	}
	return usecase.SubmitProgress{Typed: f.typed, SubmitAt: f.submitAt}
}

func (s *Server) saveSteerFlight(id, text string, typed bool, at time.Time) {
	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	if s.steerFlights == nil {
		s.steerFlights = map[string]steerFlight{}
	}
	s.steerFlights[id] = steerFlight{text: text, typed: typed, submitAt: at}
}

func (s *Server) clearSteerFlight(id string) {
	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	delete(s.steerFlights, id)
}

func (s *Server) noteSteerSettled(id string) {
	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	if s.steerSettled == nil {
		s.steerSettled = map[string]time.Time{}
	}
	s.steerSettled[id] = time.Now()
}

func (s *Server) steerSettledAt(id string) time.Time {
	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	return s.steerSettled[id]
}

func (s *Server) finishSteerSubmit(id, text string, at time.Time, err error) (time.Time, error) {
	switch {
	case err == nil:
		s.clearSteerFlight(id)
		s.noteSteerSettled(id)
		return at, nil
	case errors.Is(err, usecase.ErrSubmitUnconfirmed):
		s.saveSteerFlight(id, text, true, at)
		return time.Time{}, err
	case errors.Is(err, usecase.ErrTypedNotSubmitted):
		s.saveSteerFlight(id, text, true, time.Time{})
		return time.Time{}, err
	default:
		return time.Time{}, err
	}
}

// todoList is the session's current to-do list, empty before EnableTodos.
func (s *Server) todoList(id string) (domain.TodoList, error) {
	if s.todos == nil {
		return domain.TodoList{SessionID: id}, nil
	}
	return s.todos.Get(id)
}
