package ui

import "charm.land/lipgloss/v2"

// A1 palette. Rows, the header, and later the detail pane all use these
// so a waiting session and a "needs you" count read as the same color.
var (
	a1Wait       = lipgloss.Color("#e0a83a")
	a1Work       = lipgloss.Color("#6ea8dc")
	a1Done       = lipgloss.Color("#86b874")
	a1Prompt     = lipgloss.Color("#b994dc")
	a1Dim        = lipgloss.Color("#646e69")
	a1Faint      = lipgloss.Color("#3a4240")
	a1SelectedBg = lipgloss.Color("#222930")
	a1BoldText   = lipgloss.Color("#eef1ee")
)
