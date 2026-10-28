package ui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/server"
)

// TestDashboardFitsItsTerminal guards the height budget. The panes are sized
// from the terminal height minus a fixed allowance for the chrome around them,
// so an allowance that is too small makes the view taller than the terminal —
// which scrolls the whole dashboard — and one that is too large wastes rows.
func TestDashboardFitsItsTerminal(t *testing.T) {
	next, _ := newTestStartupModel(&startupSessionRepo{
		sessions: []domain.Session{
			{ID: "a", Name: "impl", Group: "G", RemoteHost: "regent0", TerminalName: "t1"},
			{ID: "b", Name: "review", Group: "G", RemoteHost: "regent0", TerminalName: "t2"},
		},
	}).Update(startupReadyMsg{})
	dash := next.(*Model)

	for _, size := range [][2]int{{80, 24}, {120, 30}, {154, 40}, {200, 50}, {240, 60}, {300, 24}} {
		width, height := size[0], size[1]
		dash.SetSize(width, height)
		view := dash.View().Content

		h := lipgloss.Height(view)
		w := lipgloss.Width(view)
		if h > height {
			t.Errorf("term %dx%d: view is %d rows — taller than the terminal, so it scrolls",
				width, height, h)
		}
		if w > width {
			t.Errorf("term %dx%d: view is %d columns wide", width, height, w)
		}
	}

	// A full head, eight to-dos, and a three-line question is the pane the
	// fixed budget does not cover. 80x24 is where the preview floor overflows.
	dash = withFullDetail(dash)
	dash.SetSize(80, 24)
	view := dash.View().Content
	if h := lipgloss.Height(view); h > 24 {
		t.Errorf("term 80x24 with eight to-dos and a question: view is %d rows", h)
	}
	if w := lipgloss.Width(view); w > 80 {
		t.Errorf("term 80x24 with eight to-dos and a question: view is %d columns wide", w)
	}
}

func withFullDetail(m *Model) *Model {
	now := time.Now().Add(-2 * time.Hour)
	items := make([]domain.TodoItem, 8)
	for i := range items {
		items[i] = domain.TodoItem{ID: "t", Text: "Ship the row", State: domain.TodoPending}
	}
	items[0].State = domain.TodoActive
	question := strings.Repeat("proceed with the handover ", 12)
	for i, s := range m.allSessions {
		s.CreatedAt = now
		s.AgentName = "claude"
		s.AgentModel = "opus"
		s.Branch = "feat/a1"
		s.HookState = domain.AgentStateWaitingInput
		s.HookStateMessage = question
		m.allSessions[i] = s
	}
	m.applyRemoteFilter()
	m.selectFirstSessionRow()
	m.gitStatus = domain.GitStatus{
		Ahead: 1, Behind: 1, StagedCount: 1,
		PullRequest: &domain.PullRequest{Number: 9, State: "open"},
	}
	if it, ok := m.selectedSessionItem(); ok {
		m.todos[it.session.ID] = newTodoSnapshot(domain.TodoList{
			SessionID: it.session.ID,
			Rev:       1,
			Items:     items,
		}, server.SteerStatus{})
	}
	return m
}

// The startup-check readout and the remotes line were removed; nothing should
// have brought them back, and the filter they used to report is on the list
// title instead.
func TestDashboardHasNoStartupCheckReadout(t *testing.T) {
	next, _ := newTestStartupModel(&startupSessionRepo{}).Update(startupReadyMsg{})
	dash := next.(*Model)
	dash.SetSize(200, 50)

	view := dash.View().Content
	for _, gone := range []string{"Startup Checks", "Remotes: 1 configured"} {
		if strings.Contains(view, gone) {
			t.Errorf("view still shows %q", gone)
		}
	}
}
