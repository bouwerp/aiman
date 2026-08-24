package ui

import (
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// newView wraps rendered content in a tea.View. The dashboard runs fullscreen
// with all-motion mouse tracking, so every root model returns a view with both
// enabled; for nested sub-models the flags are inert (only the root model's
// View reaches the terminal).
func newView(content string) tea.View {
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeAllMotion
	return v
}

// applyCursorStyle gives an input the pink block cursor used across setup and
// settings forms (the bubbletea v2 replacement for textinput Cursor.Style).
func applyCursorStyle(t *textinput.Model) {
	st := t.Styles()
	st.Cursor = textinput.CursorStyle{Color: lipgloss.Color("212"), Blink: true}
	t.SetStyles(st)
}
