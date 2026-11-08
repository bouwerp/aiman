package agentupdate

import "testing"

func TestManagerUpdating(t *testing.T) {
	if got := (*Manager)(nil).Updating(); got != 0 {
		t.Fatalf("nil Updating = %d, want 0", got)
	}
	m := &Manager{}
	if got := m.Updating(); got != 0 {
		t.Fatalf("fresh Updating = %d, want 0", got)
	}
	m.updating = map[string]bool{"claude": true, "codex": true, "pi": false}
	if got := m.Updating(); got != 2 {
		t.Fatalf("Updating = %d, want 2", got)
	}
	m.mu.Lock()
	delete(m.updating, "claude")
	m.mu.Unlock()
	if got := m.Updating(); got != 1 {
		t.Fatalf("after one finished: Updating = %d, want 1", got)
	}
}
