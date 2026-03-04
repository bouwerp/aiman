package ui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type BranchInputModel struct {
	textInput textinput.Model
	Confirmed bool
}

func NewBranchInputModel(proposed string) BranchInputModel {
	ti := textinput.New()
	ti.Placeholder = "Branch Name"
	ti.SetValue(proposed)
	ti.Focus()
	ti.CharLimit = 156
	ti.Width = 40

	return BranchInputModel{
		textInput: ti,
	}
}

func (m BranchInputModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m BranchInputModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			// Sanitize and validate before confirming
			value := m.sanitizeInput(m.textInput.Value())
			m.textInput.SetValue(value)
			if value != "" {
				m.Confirmed = true
				return m, nil
			}
		case " ":
			// Let space through - it will be converted to dash in sanitization
		default:
			// Block invalid characters
			if len(msg.String()) == 1 {
				if m.isInvalidChar(msg.String()) {
					return m, nil
				}
			}
		}
	}

	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)

	// Sanitize the value after each update to ensure consistency
	currentValue := m.textInput.Value()
	sanitized := m.sanitizeInput(currentValue)
	if currentValue != sanitized {
		m.textInput.SetValue(sanitized)
	}

	return m, cmd
}

// isInvalidChar checks if a character is invalid for git branch names
func (m BranchInputModel) isInvalidChar(s string) bool {
	invalidChars := `~^:\@{}[]*?|<>'!` + "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f\x7f"
	return strings.ContainsAny(s, invalidChars)
}

// sanitizeInput sanitizes the branch name input
func (m BranchInputModel) sanitizeInput(s string) string {
	if s == "" {
		return ""
	}

	// Replace spaces with dashes
	s = strings.ReplaceAll(s, " ", "-")

	// Replace underscores with dashes (mutagen compatibility)
	s = strings.ReplaceAll(s, "_", "-")

	// Remove invalid characters
	invalidPattern := regexp.MustCompile(`[\x00-\x1f\x7f~^:\\@\{\}\[\]\*\?\|<>"'!]`)
	s = invalidPattern.ReplaceAllString(s, "")

	// Remove consecutive dots
	s = regexp.MustCompile(`\.\.+`).ReplaceAllString(s, ".")

	// Remove leading dashes
	s = strings.TrimLeft(s, "-")

	// Remove trailing dots
	s = strings.TrimRight(s, ".")

	// Collapse multiple dashes
	s = regexp.MustCompile(`-+`).ReplaceAllString(s, "-")

	// Limit length for mutagen label compatibility
	const maxLen = 63
	if len(s) > maxLen {
		s = s[:maxLen]
	}

	// Remove trailing dashes after truncation
	s = strings.TrimRight(s, "-")

	return s
}

func (m BranchInputModel) View() string {
	style := lipgloss.NewStyle().Padding(1, 2).Border(lipgloss.RoundedBorder())

	return lipgloss.Place(80, 10, lipgloss.Center, lipgloss.Center,
		style.Render(fmt.Sprintf(
			"Confirm Branch Name\n\n%s\n\n(enter to confirm, esc to cancel)",
			m.textInput.View(),
		)))
}

func (m BranchInputModel) Value() string {
	return m.textInput.Value()
}
