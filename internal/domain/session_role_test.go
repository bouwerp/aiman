package domain

import "testing"

func TestSessionRoleHidden(t *testing.T) {
	cases := map[SessionRole]bool{
		SessionRoleReviewer: true,
		SessionRoleHandover: true,
		"":                  false,
		"planner":           false,
	}
	for role, want := range cases {
		if got := role.Hidden(); got != want {
			t.Errorf("%q.Hidden() = %v, want %v", role, got, want)
		}
	}
	if SessionRoleHandover != "handover" {
		t.Fatalf("SessionRoleHandover = %q", SessionRoleHandover)
	}
}

func TestHandoverChildEventKinds(t *testing.T) {
	if EventHandoverChildStarted != "handover.child_started" || EventHandoverChildFinished != "handover.child_finished" {
		t.Fatalf("kinds = %q, %q", EventHandoverChildStarted, EventHandoverChildFinished)
	}
}
