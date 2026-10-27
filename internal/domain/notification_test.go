package domain

import "testing"

func TestNeedsYou(t *testing.T) {
	for k, want := range map[NotifyKind]bool{
		NotifyWaitingInput: true, NotifyError: true, NotifyCIEscalation: true,
		NotifyReviewDone: true, NotifyEnded: false, "other": false,
	} {
		if got := NeedsYou(k); got != want {
			t.Errorf("NeedsYou(%q)=%v want %v", k, got, want)
		}
	}
}
