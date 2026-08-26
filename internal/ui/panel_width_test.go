package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// mainPanelStyle mirrors the style renderMainView builds. Kept beside the test
// that depends on its frame cost so a change to one is caught by the other.
func mainPanelStyle(mainWidth int) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, false, false, true).
		PaddingLeft(2).
		Width(mainWidth)
}

// TestMainPanelFrameMatchesTheStyle pins the constant against the style it
// describes: lipgloss counts border and padding inside Width, so the content
// area is Width minus the frame. If the border or padding changes, this fails
// rather than silently reintroducing a one-column overflow.
func TestMainPanelFrameMatchesTheStyle(t *testing.T) {
	const mainWidth = 100
	st := mainPanelStyle(mainWidth)

	if got := lipgloss.Width(st.Render("x")); got != mainWidth {
		t.Fatalf("style renders %d wide, expected Width() to be the total: %d", got, mainWidth)
	}
	// A line of exactly the content width must stay on one row.
	fits := strings.Repeat("x", mainWidth-mainPanelFrame)
	if h := lipgloss.Height(st.Render(fits)); h != 1 {
		t.Fatalf("a %d-column line wrapped inside a %d-wide panel (height %d): frame is not %d",
			len(fits), mainWidth, h, mainPanelFrame)
	}
	// One more column must wrap — proving the constant is not merely generous.
	tooWide := strings.Repeat("x", mainWidth-mainPanelFrame+1)
	if h := lipgloss.Height(st.Render(tooWide)); h != 2 {
		t.Fatalf("expected %d columns to wrap to 2 rows, got height %d", len(tooWide), h)
	}
}

// TestPreviewLinesDoNotWrapInThePanel is the reported bug: previews wrapped by a
// single character on every line. The viewport was sized to the panel width
// minus its padding, ignoring the one-column border.
func TestPreviewLinesDoNotWrapInThePanel(t *testing.T) {
	for _, width := range []int{100, 120, 154, 200, 240, 273, 300} {
		// SetSize drives sub-models that need initialising; the widths it
		// derives depend only on m.width, so they are exercised directly.
		m := &Model{width: width}

		vpW := m.mainContentWidth()
		if vpW <= 0 {
			t.Fatalf("term=%d: no content width", width)
		}
		// The fit makes every preview line run the full width of the viewport.
		line := strings.Repeat("x", vpW)
		rendered := mainPanelStyle(m.mainPanelWidth()).Render(line)
		if h := lipgloss.Height(rendered); h != 1 {
			t.Errorf("term=%d: a full-width preview line (%d cols) wrapped to %d rows in a %d-wide panel",
				width, vpW, h, m.mainPanelWidth())
		}
	}
}

// The whole row must still fit the terminal.
func TestMainViewRowFitsTheTerminal(t *testing.T) {
	h, _ := docStyle.GetFrameSize()
	for _, width := range []int{100, 154, 240, 300} {
		m := &Model{width: width}

		sidebar := strings.Repeat("s", width/3-h)
		row := lipgloss.JoinHorizontal(lipgloss.Top, sidebar,
			mainPanelStyle(m.mainPanelWidth()).Render(strings.Repeat("x", m.mainContentWidth())))
		if got := lipgloss.Width(docStyle.Render(row)); got > width {
			t.Errorf("term=%d: composed row is %d wide", width, got)
		}
	}
}
