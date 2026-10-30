package ui

import (
	"slices"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/server"
)

func sessionIDs(list []domain.Session) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		out = append(out, s.ID)
	}
	return out
}

// session_gone for a helper or child removes its row; a top-level worker
// stays listed until a scan decides.
func TestSessionGoneDropsHelperAndChildRows(t *testing.T) {
	m := eventModel(
		domain.Session{ID: "w", Name: "impl", RemoteHost: "regent0"},
		domain.Session{ID: "h", Name: "handover", Role: domain.SessionRoleHandover, ParentID: "w", RemoteHost: "regent0"},
		domain.Session{ID: "c", Name: "child", ParentID: "w", RemoteHost: "regent0"},
	)
	m.ensureSessionStreams()
	m.streams["regent0"].todoSwept = true
	for _, id := range []string{"h", "c", "w"} {
		if cmd := m.applySessionEvent(sessionEventMsg{host: "regent0", event: server.SessionEvent{Type: "session_gone", ID: id}}); cmd == nil {
			t.Fatal("session_gone stalled the stream")
		}
	}
	if got := sessionIDs(m.allSessions); !slices.Equal(got, []string{"w"}) {
		t.Fatalf("allSessions = %v, want only the worker", got)
	}
}

// A laptop row left by the role-less announcement of a handover child is
// dropped once a scan of its host finds no live terminal for it.
func TestDiscoveryDropsStaleRolelessHandoverRows(t *testing.T) {
	repo := &savingSessionRepo{startupSessionRepo: startupSessionRepo{sessions: []domain.Session{
		{ID: "w", Name: "impl", RemoteHost: "regent0", TerminalName: "WTB-1", Status: domain.SessionStatusActive},
		{ID: "stale", Name: "handover-1a2b3c4d", RemoteHost: "regent0", TerminalName: "handover-1a2b3c4d", Status: domain.SessionStatusActive},
		{ID: "stale2", Name: "x", RemoteHost: "regent0", TerminalName: "handover-deadbeef", Status: domain.SessionStatusActive},
		{ID: "user", Name: "handover-notes", RemoteHost: "regent0", TerminalName: "handover-notes", Status: domain.SessionStatusActive},
		{ID: "away", Name: "handover-0badf00d", RemoteHost: "regent1", TerminalName: "handover-0badf00d", Status: domain.SessionStatusActive},
	}}}
	cfg := testCfg()
	cfg.Remotes = append(cfg.Remotes, cfg.Remotes[0])
	cfg.Remotes[1].Host = "regent1"
	m := NewModel(cfg, nil, nil, repo, nil, nil, nil)
	m.applyDiscoveryResult(discoveryResultMsg{
		scannedHosts: map[string]bool{"regent0": true},
		sessions: []domain.Session{
			{ID: "w", RemoteHost: "regent0", TerminalName: "WTB-1", Status: domain.SessionStatusActive},
		},
	})
	slices.Sort(repo.deletedID)
	if !slices.Equal(repo.deletedID, []string{"stale", "stale2"}) {
		t.Fatalf("deleted = %v, want the two dead handover rows only", repo.deletedID)
	}
}

func TestStaleHandoverRowMatch(t *testing.T) {
	cases := []struct {
		s    domain.Session
		want bool
	}{
		{domain.Session{Name: "handover-1a2b3c4d"}, true},
		{domain.Session{TerminalName: "handover-1a2b3c4d-2"}, true},
		{domain.Session{Name: "handover-1a2b3c4d", Role: domain.SessionRoleHandover}, false},
		{domain.Session{Name: "handover-notes"}, false},
		{domain.Session{Name: "impl"}, false},
	}
	for _, c := range cases {
		if got := staleHandoverRow(c.s); got != c.want {
			t.Errorf("staleHandoverRow(%+v) = %v, want %v", c.s, got, c.want)
		}
	}
}
