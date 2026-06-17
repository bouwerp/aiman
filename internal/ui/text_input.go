package ui

import (
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type TextInputModel struct {
	textInput textinput.Model
	Confirmed bool
	Prompt    string
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

func (m TextInputModel) Update(msg tea.Msg) (TextInputModel, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEnter:
			if m.textInput.Value() != "" {
				m.Confirmed = true
				return m, nil
			}
		}
	}

	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

func (m TextInputModel) View() string {
	return lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render(m.Prompt),
		"",
		m.textInput.View(),
		"",
		lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("Press Enter to confirm, Esc to go back"),
	)
}

func (m TextInputModel) Value() string {
	return m.textInput.Value()
}
