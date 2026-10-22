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

// steerSubmitSettle is the pause between typing a note and pressing Return,
// and steerConfirmEvery the pause between checks that Return was taken.
// Variables so tests can shorten them.
var (
	steerSubmitSettle = 500 * time.Millisecond
	steerConfirmEvery = time.Second
)

// steerConfirmChecks bounds the Return retries after a PTY delivery.
const steerConfirmChecks = 3

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

// deliverSteer types text into the session's agent and submits it. Serve owns
// the terminal: a pty session is written through the runtime in this process,
// a tmux session through local send-keys.
func (s *Server) deliverSteer(ctx context.Context, id, text string) error {
	sess, ok := s.steerSession(ctx, id)
	if !ok {
		return fmt.Errorf("steer: session %s not found", id)
	}
	if !sess.IsPTY() {
		if s.remote == nil {
			return errors.New("steer: no executor for tmux delivery")
		}
		if sess.TmuxSession == "" {
			return fmt.Errorf("steer: session %s has no tmux session", id)
		}
		return usecase.SendPrompt(ctx, s.remote, sess.TmuxSession, sess.ID, text)
	}
	if s.pty == nil {
		return errors.New("steer: pty runtime is not running")
	}
	if err := s.pty.Write(sess.ID, []byte(text)); err != nil {
		return err
	}
	if err := sleepSteer(ctx, steerSubmitSettle); err != nil {
		return err
	}
	if err := s.pty.Write(sess.ID, []byte("\r")); err != nil {
		return err
	}
	// The note is typed; a failed confirm is not a failed delivery, since
	// retrying would type it a second time.
	for range steerConfirmChecks {
		if sleepSteer(ctx, steerConfirmEvery) != nil {
			return nil
		}
		screen, err := s.pty.CaptureScreen(sess.ID)
		if err != nil || !usecase.PromptStillInComposer(screen, text) {
			return nil
		}
		if err := s.pty.Write(sess.ID, []byte("\r")); err != nil {
			return nil
		}
	}
	return nil
}

// todoList is the session's current to-do list, empty before EnableTodos.
func (s *Server) todoList(id string) (domain.TodoList, error) {
	if s.todos == nil {
		return domain.TodoList{SessionID: id}, nil
	}
	return s.todos.Get(id)
}

func sleepSteer(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
