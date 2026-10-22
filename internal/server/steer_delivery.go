package server

import (
	"context"
	"errors"
	"fmt"
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
)

// steerClassifierQuiet is how long after a delivery the pane classifier must
// wait before an idle screen counts. The agent may not have redrawn yet, and
// the screen it shows is the one from before the note went in.
const steerClassifierQuiet = 3 * time.Second

// steerSubmitTiming is the PTY submit budget: the remote prompt path's proven
// values, whose worst case (about 12 s) fits inside steerOpTimeout. A variable
// so tests can shorten it.
var steerSubmitTiming = usecase.DefaultSubmitTiming()

// steerPromptTag keeps steering's tmux prompt file apart from the laptop's.
const steerPromptTag = "steer"

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
// Working and permission or choice dialogs hold it, as does an ended agent.
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
	if sess.AgentEnded {
		return false, steerHeldEnded
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
	if sess.IsPTY() && s.pty == nil {
		return false, steerHeldUnknown
	}
	st, conf := s.classify(ctx, sess)
	switch {
	case st == domain.AgentStateWaitingInput:
		return false, steerHeldDialog
	case st == domain.AgentStateIdle && conf == pane.High && time.Since(lastDelivered) > steerClassifierQuiet:
		return true, ""
	default:
		return false, steerHeldUnknown
	}
}

// deliverSteer types text into the session's agent and submits it, and
// returns when it pressed Return. Serve owns the terminal: a pty session is
// written through the runtime in this process, a tmux session through local
// send-keys.
func (s *Server) deliverSteer(ctx context.Context, id, text string) (time.Time, error) {
	sess, ok := s.steerSession(ctx, id)
	if !ok {
		return time.Time{}, fmt.Errorf("steer: session %s not found", id)
	}
	if !sess.IsPTY() {
		if s.remote == nil {
			return time.Time{}, errors.New("steer: no executor for tmux delivery")
		}
		if sess.TmuxSession == "" {
			return time.Time{}, fmt.Errorf("steer: session %s has no tmux session", id)
		}
		// The script returns right after its Enter, so this is the submit time
		// to within a round trip.
		if err := usecase.SendPromptTagged(ctx, s.remote, sess.TmuxSession, sess.ID, steerPromptTag, text); err != nil {
			return time.Time{}, err
		}
		return time.Now(), nil
	}
	if s.pty == nil {
		return time.Time{}, errors.New("steer: pty runtime is not running")
	}
	write := func(b string) func(context.Context) error {
		return func(context.Context) error { return s.pty.Write(sess.ID, []byte(b)) }
	}
	return usecase.TypeAndSubmit(ctx, text, steerSubmitTiming, write(text), write("\r"),
		func(context.Context) (string, error) { return s.pty.CaptureScreen(sess.ID) })
}

// todoList is the session's current to-do list, empty before EnableTodos.
func (s *Server) todoList(id string) (domain.TodoList, error) {
	if s.todos == nil {
		return domain.TodoList{SessionID: id}, nil
	}
	return s.todos.Get(id)
}
