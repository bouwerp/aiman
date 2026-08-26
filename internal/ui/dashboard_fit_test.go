package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/bouwerp/aiman/internal/domain"
)

// TestDashboardFitsItsTerminal guards the height budget. The panes are sized
// from the terminal height minus a fixed allowance for the chrome around them,
// so an allowance that is too small makes the view taller than the terminal —
// which scrolls the whole dashboard — and one that is too large wastes rows.
func TestDashboardFitsItsTerminal(t *testing.T) {
	next, _ := newTestStartupModel(&startupSessionRepo{
		sessions: []domain.Session{
			{ID: "a", Name: "impl", Group: "G", RemoteHost: "regent0", TmuxSession: "t1"},
			{ID: "b", Name: "review", Group: "G", RemoteHost: "regent0", TmuxSession: "t2"},
		},
	}).Update(startupReadyMsg{})
	dash := next.(*Model)

	for _, size := range [][2]int{{120, 30}, {154, 40}, {200, 50}, {240, 60}, {300, 24}} {
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
