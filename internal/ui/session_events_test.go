package ui

import (
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/pane"
	"github.com/bouwerp/aiman/internal/server"
)

func eventModel(sessions ...domain.Session) *Model {
	return &Model{
		cfg:         &config.Config{Remotes: []config.Remote{{Host: "regent0", User: "code"}}},
		allSessions: sessions,
		streams:     map[string]*sessionStream{},
		eventSeen:   map[string]sessionEventState{},
	}
}

// A remote with no sessions yet still needs a stream, or the first CLI create
// never reaches the dashboard.
func TestEnsureSessionStreamsOpensForConfiguredRemotes(t *testing.T) {
	m := eventModel()
	if cmd := m.ensureSessionStreams(); cmd == nil {
		t.Fatal("a configured remote should open a stream even with no sessions")
	}
	if _, ok := m.streams["regent0"]; !ok {
		t.Errorf("expected a stream for regent0, got %v", m.streams)
	}
	before := len(m.streams)
	if cmd := m.ensureSessionStreams(); cmd != nil {
		t.Error("an existing stream should not be reopened")
	}
	if len(m.streams) != before {
		t.Errorf("stream count changed: %d -> %d", before, len(m.streams))
	}

	m.allSessions = nil
	m.ensureSessionStreams()
	if len(m.streams) != 1 {
		t.Errorf("empty session list must not close the remote stream, got %d", len(m.streams))
	}
}

func TestApplySessionEventInsertsCreatedSession(t *testing.T) {
	m := eventModel(domain.Session{ID: "parent", Name: "impl", RemoteHost: "regent0", Group: "WTB-1"})
	m.ensureSessionStreams()
	info := &server.SessionInfo{ID: "child", Name: "reviewer", Group: "WTB-1", ParentID: "parent", Status: "ACTIVE"}
	if cmd := m.applySessionEvent(sessionEventMsg{
		host:  "regent0",
		event: server.SessionEvent{Type: "session_created", ID: "child", Session: info},
	}); cmd == nil {
		t.Fatal("stream would stall")
	}
	found := false
	for _, s := range m.allSessions {
		if s.ID == "child" && s.ParentID == "parent" && s.Name == "reviewer" {
			found = true
		}
	}
	if !found {
		t.Fatalf("created session missing from list: %+v", m.allSessions)
	}
}

// The stream has to keep being consumed: every handled event must queue the wait
// for the next one, or one event stalls it forever.
func TestApplySessionEventAlwaysQueuesTheNextWait(t *testing.T) {
	m := eventModel(domain.Session{ID: "p", RemoteHost: "regent0", Backend: domain.BackendPTY})
	m.ensureSessionStreams()

	for _, ev := range []server.SessionEvent{
		{Type: "keepalive"},
		{Type: "session_activity", ID: "p"},
		{Type: "session_activity", ID: "unknown-session"},
		{Type: "session_gone", ID: "p"},
	} {
		if cmd := m.applySessionEvent(sessionEventMsg{host: "regent0", event: ev}); cmd == nil {
			t.Errorf("event %+v returned no command, so the stream would stall", ev)
		}
	}
}

// An event for a stream that has been stopped must not resurrect it.
func TestApplySessionEventIgnoresStoppedStreams(t *testing.T) {
	m := eventModel()
	if cmd := m.applySessionEvent(sessionEventMsg{host: "gone", event: server.SessionEvent{ID: "x"}}); cmd != nil {
		t.Error("an event for an unknown stream should not queue more work")
	}
}

// A title that moved a moment ago is direct evidence of work, so it shows
// immediately rather than waiting for the next classification.
func TestMarkSessionBusyFromEventNeedsARecentTitleChange(t *testing.T) {
	m := eventModel()
	if cmd := m.markSessionBusyFromEvent("p", sessionEventState{}); cmd != nil {
		t.Error("no title information must not mark anything busy")
	}
	stale := sessionEventState{titleChanged: time.Now().Add(-pane.TitleActivityWindow - time.Second)}
	if cmd := m.markSessionBusyFromEvent("p", stale); cmd != nil {
		t.Error("a stale title must not mark a session busy")
	}
}

func TestParseEventTime(t *testing.T) {
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if got := parseEventTime(stamp); got.IsZero() {
		t.Errorf("a valid stamp should parse: %q", stamp)
	}
	for _, bad := range []string{"", "   ", "not a time"} {
		if got := parseEventTime(bad); !got.IsZero() {
			t.Errorf("%q should be zero, got %v", bad, got)
		}
	}
}

func TestRemoteByHost(t *testing.T) {
	cfg := &config.Config{Remotes: []config.Remote{{Host: "a"}, {Host: "b", User: "code"}}}
	if r, ok := remoteByHost(cfg, "b"); !ok || r.User != "code" {
		t.Errorf("got %+v / %v", r, ok)
	}
	if _, ok := remoteByHost(cfg, "nope"); ok {
		t.Error("an unknown host must not resolve")
	}
}

// A child session created by an in-pane agent reaches the dashboard through
// serve's live stream and nothing else. Dropping Backend there made it read as
// tmux, so pane capture asked tmux for a session that lives in the PTY runtime
// and the preview never left "Loading…".
func TestSessionFromInfoKeepsTheBackend(t *testing.T) {
	s := sessionFromInfo("regent0", server.SessionInfo{
		ID:          "child-1",
		Name:        "review-treasury-prs",
		ParentID:    "parent-1",
		TmuxSession: "fix-yield",
		Backend:     domain.BackendPTY,
	})
	if !s.IsPTY() {
		t.Errorf("backend dropped: got %q", s.Backend)
	}
	if s.ParentID != "parent-1" {
		t.Errorf("parent dropped: %+v", s)
	}
}
