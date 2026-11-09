package server

import (
	"context"
	"fmt"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// With a recorder listening, a fresh idle report is captured for the question
// check, and that capture counts toward the pass's budget.
func TestSweepRecorderIdleGroupCountsTowardTheBudget(t *testing.T) {
	r := newSweepRig(t)
	for i := range 20 {
		r.session(t, fmt.Sprintf("idle-%02d", i), domain.AgentStateIdle, "❯ ")
	}
	r.srv.sweepStates(context.Background())
	if n := r.pty.Captures(); n != sweepCaptureBudget {
		t.Fatalf("captures = %d, want %d", n, sweepCaptureBudget)
	}
}

// Stale and working sessions are served before fresh idle ones.
func TestSweepServesStaleAndWorkingBeforeRecorderIdle(t *testing.T) {
	r := newSweepRig(t)
	var stale, working, idle []string
	for i := range 8 {
		stale = append(stale, fmt.Sprintf("stale-%d", i))
		working = append(working, fmt.Sprintf("work-%d", i))
		idle = append(idle, fmt.Sprintf("idle-%d", i))
	}
	// Idle rows first, so list order alone would serve them first.
	for _, id := range idle {
		r.session(t, id, domain.AgentStateIdle, "❯ ")
	}
	for _, id := range stale {
		r.session(t, id, "", "● thinking (12s · esc to interrupt)")
	}
	for _, id := range working {
		r.session(t, id, domain.AgentStateWorking, "● thinking")
	}
	r.srv.sweepStates(context.Background())
	if n := r.pty.Captures(); n != sweepCaptureBudget {
		t.Fatalf("captures = %d, want %d", n, sweepCaptureBudget)
	}
	for _, id := range append(stale, working...) {
		if c := r.pty.CapturesOf(id); c != 1 {
			t.Errorf("%s captured %d times, want 1", id, c)
		}
	}
	for _, id := range idle {
		if c := r.pty.CapturesOf(id); c != 0 {
			t.Errorf("%s (fresh idle) captured %d times before stale and working were served", id, c)
		}
	}
}
