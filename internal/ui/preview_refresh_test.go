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
	s := domain.Session{ID: "s1", Backend: domain.BackendPTY, TmuxSession: "s1", RemoteHost: "10.0.1.5"}
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

func TestPreviewPollDoesNotOverlap(t *testing.T) {
	cfg := twoRemoteCfg()
	s := domain.Session{ID: "s1", TmuxSession: "s1", RemoteHost: "10.0.1.5"}
	m := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	m.previewBusy = true
	if cmd := m.queuePreview(s); cmd != nil {
		t.Fatal("a poll already on the wire should not start another")
	}
}

func TestUnchangedPreviewKeepsTheViewport(t *testing.T) {
	cfg := twoRemoteCfg()
	s := domain.Session{ID: "s1", Name: "impl", TmuxSession: "impl", RemoteHost: "10.0.1.5"}
	m := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	m.applyRemoteFilter()
	m.activeSession = "impl"
	m.tmuxOutput = "old screen"
	m.previewBusy = true
	_ = m.applyPreviewRefresh(previewRefreshMsg{
		session: "impl", id: "s1", unchanged: true, text: "old screen",
		cache: usecase.PaneCache{Hash: "h", HistorySize: 3, Text: "old screen"},
	})
	if m.previewBusy || m.tmuxOutput != "old screen" {
		t.Fatalf("busy=%v out=%q", m.previewBusy, m.tmuxOutput)
	}
	if m.paneCache["s1"].Hash != "h" || m.paneCache["s1"].HistorySize != 3 {
		t.Fatalf("cache=%+v", m.paneCache["s1"])
	}
}

func TestChangedPreviewReplacesTheViewport(t *testing.T) {
	cfg := twoRemoteCfg()
	s := domain.Session{ID: "s1", Name: "impl", TmuxSession: "impl", RemoteHost: "10.0.1.5"}
	m := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	m.applyRemoteFilter()
	m.activeSession = "impl"
	m.tmuxOutput = "old screen"
	m.previewBusy = true
	_ = m.applyPreviewRefresh(previewRefreshMsg{
		session: "impl", id: "s1", text: "new screen",
		cache: usecase.PaneCache{Text: "new screen", Hash: "n"},
	})
	if m.previewBusy || m.tmuxOutput != "new screen" {
		t.Fatalf("busy=%v out=%q", m.previewBusy, m.tmuxOutput)
	}
}

func TestTmuxPreviewStillProbes(t *testing.T) {
	cfg := twoRemoteCfg()
	s := domain.Session{ID: "s1", TmuxSession: "s1", RemoteHost: "10.0.1.5"}
	m := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	if cmd := m.queuePreview(s); cmd == nil || !m.previewBusy {
		t.Fatal("tmux has no event stream, so the tick still probes")
	}
}
