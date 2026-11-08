package awslogin

import "testing"

func TestManagerPending(t *testing.T) {
	if got := (*Manager)(nil).Pending(); got != 0 {
		t.Fatalf("nil Pending = %d, want 0", got)
	}
	m := New(Options{Home: t.TempDir()})
	t.Cleanup(m.Close)
	states := []string{
		StatePending, StateApproved, StateMinting,
		StateDone, StateDenied, StateExpired, StateFailed,
	}
	for i, s := range states {
		m.logins[string(rune('a'+i))] = &record{login: Login{State: s}}
	}
	if got := m.Pending(); got != 3 {
		t.Fatalf("Pending = %d, want 3 (pending, approved, minting)", got)
	}
}
