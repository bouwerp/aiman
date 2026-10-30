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

// session_gone removes a hidden helper's row. A visible child (a --for child
// or a PR review session) and a top-level worker stay listed until a scan
// decides.
func TestSessionGoneDropsOnlyHiddenHelperRows(t *testing.T) {
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
	if got := sessionIDs(m.allSessions); !slices.Equal(got, []string{"w", "c"}) {
		t.Fatalf("allSessions = %v, want the worker and the visible child", got)
	}
}

// A consult row first announced without its role is hidden once serve
// announces it again with the role: the live role wins over the stored empty
// one.
func TestReannouncedRoleHidesStaleRow(t *testing.T) {
	m := NewModel(testCfg(), nil, nil, &savingSessionRepo{}, nil, nil, nil)
	m.allSessions = []domain.Session{
		{ID: "w", Name: "impl", RemoteHost: "regent0"},
		{ID: "k", Name: "adhoc-20261030T101500", TerminalName: "adhoc-20261030T101500", RemoteHost: "regent0"},
	}
	m.ensureSessionStreams()
	m.streams["regent0"].todoSwept = true
	info := server.SessionInfo{ID: "k", Name: "consult", Role: string(domain.SessionRoleConsult), ParentID: "w", TerminalName: "adhoc-20261030T101500"}
	m.applySessionEvent(sessionEventMsg{host: "regent0", event: server.SessionEvent{Type: "session_created", ID: "k", Session: &info}})
	got, ok := m.sessionByID("k")
	if !ok || got.Role != domain.SessionRoleConsult {
		t.Fatalf("row = %+v, want role consult", got)
	}
	m.applyRemoteFilter()
	listed := map[string]bool{}
	for _, it := range m.list.Items() {
		if row, ok := it.(item); ok {
			listed[row.session.ID] = true
		}
	}
	if !listed["w"] || listed["k"] {
		t.Fatalf("listed = %v, want the worker and not the consult row", listed)
	}
}

// A role-less row whose terminal is named like a handover child but is live
// is the user's own session and stays stored.
func TestDiscoveryKeepsLiveRolelessHandoverNamedRow(t *testing.T) {
	repo := &savingSessionRepo{startupSessionRepo: startupSessionRepo{sessions: []domain.Session{
		{ID: "mine", Name: "handover-1a2b3c4d", RemoteHost: "regent0", TerminalName: "handover-1a2b3c4d", Status: domain.SessionStatusActive},
	}}}
	m := NewModel(testCfg(), nil, nil, repo, nil, nil, nil)
	m.applyDiscoveryResult(discoveryResultMsg{
		scannedHosts: map[string]bool{"regent0": true},
		sessions: []domain.Session{
			{ID: "mine", RemoteHost: "regent0", TerminalName: "handover-1a2b3c4d", Status: domain.SessionStatusActive},
		},
	})
	if len(repo.deletedID) != 0 {
		t.Fatalf("deleted = %v, want the live row kept", repo.deletedID)
	}
	if _, ok := m.sessionByID("mine"); !ok {
		t.Fatal("live row dropped from the list")
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
