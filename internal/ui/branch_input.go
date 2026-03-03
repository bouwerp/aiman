package ui

import (
	"fmt"

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
			if m.textInput.Value() != "" {
				m.Confirmed = true
				return m, nil
			}
		}
	}

	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
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
