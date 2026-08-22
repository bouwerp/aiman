package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type TextInputModel struct {
	textInput textinput.Model
	Confirmed bool
	Prompt    string
	Error     string
}

func NewTextInputModel(prompt, placeholder, initial string) TextInputModel {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.Focus()
	ti.CharLimit = 156
	ti.Width = 40
	ti.SetValue(initial)

	return TextInputModel{
		textInput: ti,
		Prompt:    prompt,
	}
}

func (m TextInputModel) Init() tea.Cmd {
	return textinput.Blink
}

func isConfirmKey(km tea.KeyMsg) bool {
	switch km.String() {
	case "enter", "ctrl+j", "ctrl+m":
		return true
	}
	return km.Type == tea.KeyEnter
}

func (m TextInputModel) Update(msg tea.Msg) (TextInputModel, tea.Cmd) {
	var cmd tea.Cmd

	if km, ok := msg.(tea.KeyMsg); ok && isConfirmKey(km) {
		if strings.TrimSpace(m.textInput.Value()) != "" {
			m.Confirmed = true
			m.Error = ""
			return m, nil
		}
	}

	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

func (m TextInputModel) View() string {
	lines := []string{
		titleStyle.Render(m.Prompt),
		"",
		m.textInput.View(),
		"",
	}
	if m.Error != "" {
		lines = append(lines, failStyle.Render(m.Error), "")
	}
	lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("Press Enter to confirm, Esc to go back"))
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func (m TextInputModel) Value() string {
	return m.textInput.Value()
}
