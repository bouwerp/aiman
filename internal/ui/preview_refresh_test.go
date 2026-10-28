package ui

import (
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/usecase"
)

func TestParseEventTimeAcceptsFractionalSeconds(t *testing.T) {
	got := parseEventTime("2026-10-08T12:00:00.5Z")
	if got.IsZero() {
		t.Fatal("fractional timestamp was dropped")
	}
}

func TestPTYPreviewSkipsWhenOutputIsUnchanged(t *testing.T) {
	cfg := twoRemoteCfg()
	s := domain.Session{ID: "s1", Backend: domain.BackendPTY, TerminalName: "s1", RemoteHost: "10.0.1.5"}
	m := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	when := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	m.paneCache[s.ID] = usecase.PaneCache{Text: "screen", OutputAt: when}
	m.eventSeen = map[string]sessionEventState{s.ID: {lastOutput: when}}
	if cmd := m.queuePreview(s); cmd != nil {
		t.Fatal("unchanged pty output should not start a poll")
	}
	if m.previewBusy {
		t.Fatal("a skipped poll must not be marked in flight")
	}
}

// Selecting a session blanks the panel to "Loading..." and then asks for a
// poll. When the event stream says that screen has not moved, the poll is
// skipped. The capture is already in memory; leaving the placeholder up makes
// the preview look stuck.
func TestUnchangedPreviewPaintsTheCachedScreen(t *testing.T) {
	cfg := twoRemoteCfg()
	s := domain.Session{ID: "s1", Backend: domain.BackendPTY, TerminalName: "s1", RemoteHost: "10.0.1.5"}
	m := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	when := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	m.paneCache[s.ID] = usecase.PaneCache{Text: "cached screen", OutputAt: when}
	m.eventSeen = map[string]sessionEventState{s.ID: {lastOutput: when}}
	m.activeSession = previewKey(s)
	m.paneOutput = "Loading..."

	if cmd := m.queuePreview(s); cmd != nil {
		t.Fatal("unchanged pty output should not start a poll")
	}
	if m.paneOutput != "cached screen" {
		t.Fatalf("preview = %q, want the cached screen", m.paneOutput)
	}
}

// A poll already on the wire must not be joined by another, but the session
// just selected still has a frame. The placeholder stays only when there is
// nothing to show.
func TestBusyPollShowsTheCachedScreen(t *testing.T) {
	cfg := twoRemoteCfg()
	s := domain.Session{ID: "s1", Backend: domain.BackendPTY, TerminalName: "s1", RemoteHost: "10.0.1.5"}
	m := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	later := time.Date(2026, 10, 8, 12, 5, 0, 0, time.UTC)
	m.paneCache[s.ID] = usecase.PaneCache{Text: "cached screen", OutputAt: later.Add(-time.Minute)}
	m.eventSeen = map[string]sessionEventState{s.ID: {lastOutput: later}}
	m.activeSession = previewKey(s)
	m.paneOutput = "Loading..."
	m.previewBusy = true

	if cmd := m.queuePreview(s); cmd != nil || !m.previewBusy {
		t.Fatalf("cmd=%v busy=%v", cmd != nil, m.previewBusy)
	}
	if m.paneOutput != "cached screen" {
		t.Fatalf("preview = %q, want the cached screen", m.paneOutput)
	}
}

func TestPreviewPollDoesNotOverlap(t *testing.T) {
	cfg := twoRemoteCfg()
	s := domain.Session{ID: "s1", TerminalName: "s1", RemoteHost: "10.0.1.5"}
	m := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	m.previewBusy = true
	if cmd := m.queuePreview(s); cmd != nil {
		t.Fatal("a poll already on the wire should not start another")
	}
}

func TestUnchangedPreviewKeepsTheViewport(t *testing.T) {
	cfg := twoRemoteCfg()
	s := domain.Session{ID: "s1", Name: "impl", TerminalName: "impl", RemoteHost: "10.0.1.5"}
	m := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	m.applyRemoteFilter()
	m.activeSession = previewKey(s)
	m.paneOutput = "old screen"
	m.previewBusy = true
	_ = m.applyPreviewRefresh(previewRefreshMsg{
		session: "impl", id: "s1", unchanged: true, text: "old screen",
		cache: usecase.PaneCache{Hash: "h", Text: "old screen"},
	})
	if m.previewBusy || m.paneOutput != "old screen" {
		t.Fatalf("busy=%v out=%q", m.previewBusy, m.paneOutput)
	}
	if m.paneCache["s1"].Hash != "h" {
		t.Fatalf("cache=%+v", m.paneCache["s1"])
	}
}

func TestChangedPreviewReplacesTheViewport(t *testing.T) {
	cfg := twoRemoteCfg()
	s := domain.Session{ID: "s1", Name: "impl", TerminalName: "impl", RemoteHost: "10.0.1.5"}
	m := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	m.applyRemoteFilter()
	m.activeSession = previewKey(s)
	m.paneOutput = "old screen"
	m.previewBusy = true
	_ = m.applyPreviewRefresh(previewRefreshMsg{
		session: "impl", id: "s1", text: "new screen",
		cache: usecase.PaneCache{Text: "new screen", Hash: "n"},
	})
	if m.previewBusy || m.paneOutput != "new screen" {
		t.Fatalf("busy=%v out=%q", m.previewBusy, m.paneOutput)
	}
}

func TestTmuxPreviewStillProbes(t *testing.T) {
	cfg := twoRemoteCfg()
	s := domain.Session{ID: "s1", TerminalName: "s1", RemoteHost: "10.0.1.5"}
	m := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	if cmd := m.queuePreview(s); cmd == nil || !m.previewBusy {
		t.Fatal("tmux has no event stream, so the tick still probes")
	}
}
