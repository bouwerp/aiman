package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/bouwerp/aiman/internal/domain"
)

type BranchInputModel struct {
	textInput textinput.Model
	Confirmed bool
	labelMode bool // true when collecting an ad-hoc session label rather than a git branch
}

func NewBranchInputModel(proposed string) BranchInputModel {
	return newBranchInputModelMode(proposed, false)
}

func NewAdHocLabelInputModel(proposed string) BranchInputModel {
	return newBranchInputModelMode(proposed, true)
}

func newBranchInputModelMode(proposed string, labelMode bool) BranchInputModel {
	ti := textinput.New()
	if labelMode {
		ti.Placeholder = "e.g. debug prod logs"
	} else {
		ti.Placeholder = "Branch Name"
	}
	ti.Focus()
	ti.CharLimit = 120
	ti.SetWidth(40)

	m := BranchInputModel{
		textInput: ti,
		labelMode: labelMode,
	}

	m.textInput.SetValue(strings.TrimSpace(proposed))

	return m
}

func (m BranchInputModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m BranchInputModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(tea.KeyPressMsg); ok && msg.String() == "enter" {
		value := m.Name()
		m.textInput.SetValue(value)
		if domain.ValidateSessionName(value) == nil && m.BranchName() != "" {
			m.Confirmed = true
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)

	return m, cmd
}

// sanitizeInput normalizes the display name for Git identifiers.
func (m BranchInputModel) sanitizeInput(s string) string {
	s = domain.SanitizeBranchName(s)
	if s == "" {
		return ""
	}
	const maxLen = 63
	if len(s) > maxLen {
		s = s[:maxLen]
	}
	return strings.TrimRight(s, "-")
}

func (m BranchInputModel) viewString() string {
	style := lipgloss.NewStyle().Padding(1, 2).Border(lipgloss.RoundedBorder())

	title := "New Session Name"
	if m.labelMode {
		title = "Ad-hoc Session Name"
	}
	identifier := m.BranchName()
	if identifier == "" {
		identifier = "(enter a name with letters or numbers)"
	}
	return lipgloss.Place(80, 10, lipgloss.Center, lipgloss.Center,
		style.Render(fmt.Sprintf(
			"%s\n\n%s\n\nBranch: %s\nWorktree: <repository>@%s\nTmux session: %s\n\n(enter to confirm, esc to cancel)",
			title,
			m.textInput.View(),
			identifier,
			identifier,
			domain.SanitizeTmuxSessionName(identifier),
		)))
}

// Name returns the user-visible session name.
func (m BranchInputModel) Name() string {
	return strings.TrimSpace(m.textInput.Value())
}

// BranchName returns the Git-safe identifier derived from the session name.
func (m BranchInputModel) BranchName() string {
	return m.sanitizeInput(m.Name())
}

func (m BranchInputModel) Value() string {
	return m.BranchName()
}

func (m BranchInputModel) View() tea.View {
	return newView(m.viewString())
}
