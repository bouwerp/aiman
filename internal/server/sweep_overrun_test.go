package server

import "testing"

func TestCountOverrunAddsDroppedTicks(t *testing.T) {
	s := &Server{}
	s.countOverrun(notifySweepEvery / 2)
	s.countOverrun(notifySweepEvery)
	if got := s.SweepSkipped(); got != 0 {
		t.Fatalf("SweepSkipped = %d after sweeps within the period, want 0", got)
	}
	s.countOverrun(notifySweepEvery*3 + notifySweepEvery/2)
	if got := s.SweepSkipped(); got != 3 {
		t.Fatalf("SweepSkipped = %d after a sweep of 3.5 periods, want 3", got)
	}
}
