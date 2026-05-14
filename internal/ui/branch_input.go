package ui

import (
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	ti.CharLimit = 156
	ti.Width = 40

	m := BranchInputModel{
		textInput: ti,
		labelMode: labelMode,
	}

	sanitized := m.sanitizeInput(proposed)
	m.textInput.SetValue(sanitized)

	return m
}

func (m BranchInputModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m BranchInputModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "enter":
			// Sanitize and validate before confirming
			value := m.sanitizeFinal(m.textInput.Value())
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

// isInvalidChar blocks typing chars that can never appear in a sanitized branch
// (everything else is normalized on update via domain.SanitizeBranchName).
func (m BranchInputModel) isInvalidChar(s string) bool {
	invalidChars := `~^:\@{}[]*?<>'!,` + "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f\x7f"
	return strings.ContainsAny(s, invalidChars)
}

// sanitizeInput normalizes branch names to git-safe characters (see domain.SanitizeBranchName).
// When finalizing (e.g. on enter), pass finalize=true to also strip trailing separators.
func (m BranchInputModel) sanitizeInput(s string) string {
	s = domain.SanitizeBranchName(s)
	if s == "" {
		return ""
	}
	const maxLen = 63
	if len(s) > maxLen {
		s = s[:maxLen]
	}
	return s
}

// sanitizeFinal applies sanitizeInput and also trims trailing separators (- and _)
// that are invalid at the end of a branch name. Call only on confirmation.
func (m BranchInputModel) sanitizeFinal(s string) string {
	s = m.sanitizeInput(s)
	return strings.TrimRight(s, "-_")
}

func (m BranchInputModel) View() string {
	style := lipgloss.NewStyle().Padding(1, 2).Border(lipgloss.RoundedBorder())

	title := "Confirm Branch Name"
	if m.labelMode {
		title = "Session Label"
	}
	return lipgloss.Place(80, 10, lipgloss.Center, lipgloss.Center,
		style.Render(fmt.Sprintf(
			"%s\n\n%s\n\n(enter to confirm, esc to cancel)",
			title,
			m.textInput.View(),
		)))
}

func (m BranchInputModel) Value() string {
	return m.textInput.Value()
}
