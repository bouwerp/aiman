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

// Only PTY sessions publish activity, so a remote with none has nothing to
// stream and must not hold an SSH connection open for it.
func TestEnsureSessionStreamsOnlyForPTYRemotes(t *testing.T) {
	m := eventModel(domain.Session{ID: "t", RemoteHost: "regent0", TmuxSession: "x"})
	if cmd := m.ensureSessionStreams(); cmd != nil {
		t.Error("a tmux-only remote should not open a stream")
	}
	if len(m.streams) != 0 {
		t.Errorf("expected no streams, got %d", len(m.streams))
	}

	m = eventModel(domain.Session{ID: "p", RemoteHost: "regent0", Backend: domain.BackendPTY})
	if cmd := m.ensureSessionStreams(); cmd == nil {
		t.Fatal("a PTY session should open a stream for its remote")
	}
	if _, ok := m.streams["regent0"]; !ok {
		t.Errorf("expected a stream for regent0, got %v", m.streams)
	}

	// Already running: no second stream for the same remote.
	before := len(m.streams)
	if cmd := m.ensureSessionStreams(); cmd != nil {
		t.Error("an existing stream should not be reopened")
	}
	if len(m.streams) != before {
		t.Errorf("stream count changed: %d -> %d", before, len(m.streams))
	}
}

// When the last PTY session on a remote goes away, its stream is closed.
func TestEnsureSessionStreamsClosesUnneededStreams(t *testing.T) {
	m := eventModel(domain.Session{ID: "p", RemoteHost: "regent0", Backend: domain.BackendPTY})
	m.ensureSessionStreams()
	if len(m.streams) != 1 {
		t.Fatalf("expected one stream, got %d", len(m.streams))
	}

	m.allSessions = nil
	m.ensureSessionStreams()
	if len(m.streams) != 0 {
		t.Errorf("expected the stream to be closed, got %d", len(m.streams))
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
