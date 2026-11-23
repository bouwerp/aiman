package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// runThroughUpdate runs cmd and hands every message it yields to Update, as
// the program does, following the commands Update returns. Ticks and waits
// are not followed.
func runThroughUpdate(m *Model, cmd tea.Cmd, depth int) {
	if cmd == nil || depth > 6 {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(300 * time.Millisecond):
		return
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			runThroughUpdate(m, c, depth+1)
		}
		return
	}
	if msg == nil {
		return
	}
	_, next := m.Update(msg)
	runThroughUpdate(m, next, depth+1)
}

// The startup read reaches the header through Update, not only through the
// remote-service router.
func TestOverviewBannerThroughUpdate(t *testing.T) {
	now := time.Now()
	fakeOverviewByHost(t, func(host, method string, _ map[string]any) any {
		if host != "10.0.1.9" {
			return nil
		}
		return latestJSON("done", now.Add(-time.Minute), now, 3, "", stepsJSON("done", "done", "done", "done", "done"))
	})
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	runThroughUpdate(m, m.Init(), 0)
	if top := ansi.Strip(strings.SplitN(m.View().Content, "\n", 4)[1]); !strings.Contains(top, "3 need you") {
		t.Fatalf("header %q lacks the banner", top)
	}
}

// The Overview page gets its run and its steps through Update.
func TestOverviewPageThroughUpdate(t *testing.T) {
	now := time.Now()
	run := fmt.Sprintf(`{"type":"overview_run","run":{"id":"r9","agent":"claude","state":"running","started_at":%q,"sessions":4,"steps":%s}}`,
		now.Add(-40*time.Second).UTC().Format(time.RFC3339), stepsJSON("done", "done", "running", "pending", "pending"))
	fakeOverviewByHost(t, func(_, method string, p map[string]any) any {
		if method != "overview.get" {
			return nil
		}
		if p["summary"] == true {
			return latestJSON("running", now.Add(-40*time.Second), time.Time{}, 0, "", stepsJSON("done", "done", "running", "pending", "pending"))
		}
		return run
	})
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	runThroughUpdate(m, m.enterOverview(), 0)
	out := ansi.Strip(m.View().Content)
	if !strings.Contains(out, "▸") || !strings.Contains(out, "start agent") {
		t.Fatalf("page lacks the running step:\n%s", out)
	}
}
