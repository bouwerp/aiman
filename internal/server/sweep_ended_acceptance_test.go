package server

import (
	"context"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// Acceptance test for t101: an agent that ended is answered by the flag
// alone, so the sweep never captures its screen for that transition. Do not
// edit.

func TestSweepEndedTransitionCapturesNothing(t *testing.T) {
	r := newSweepRig(t)
	ctx := context.Background()
	sess := r.session(t, "s1", domain.AgentStateWorking, statementFrame)
	r.srv.sweepStates(ctx)
	before := r.pty.CapturesOf("s1")
	sess.AgentEnded = true
	if err := r.repo.Save(ctx, &sess); err != nil {
		t.Fatal(err)
	}
	r.srv.sweepStates(ctx)
	if after := r.pty.CapturesOf("s1"); after != before {
		t.Fatalf("ended transition captured the screen: %d captures, was %d", after, before)
	}
	ended := 0
	for _, n := range r.store.rows {
		if n.Kind == domain.NotifyEnded {
			ended++
		}
	}
	if ended != 1 {
		t.Fatalf("ended notifications = %d, want 1: %+v", ended, r.store.rows)
	}
}

// A waiting_input transition still captures: the menu behind it is the
// notification's reason.
func TestSweepWaitingInputTransitionStillCaptures(t *testing.T) {
	r := newSweepRig(t)
	ctx := context.Background()
	sess := r.session(t, "s1", domain.AgentStateWorking, realMenu)
	r.srv.sweepStates(ctx)
	before := r.pty.CapturesOf("s1")
	sess.HookState = domain.AgentStateWaitingInput
	if err := r.repo.Save(ctx, &sess); err != nil {
		t.Fatal(err)
	}
	r.srv.sweepStates(ctx)
	if after := r.pty.CapturesOf("s1"); after <= before {
		t.Fatalf("waiting_input transition did not capture: %d captures, was %d", after, before)
	}
}
