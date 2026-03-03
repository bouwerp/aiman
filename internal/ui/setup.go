package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/bouwerp/aiman/internal/infra/config"
)

type SetupModel struct {
	cfg        *config.Config
	inputs     []textinput.Model
	focusIndex int
	err        error
	saved      bool
}

func NewSetupModel(cfg *config.Config) SetupModel {
	m := SetupModel{
		cfg:    cfg,
		inputs: make([]textinput.Model, 3),
	}

	var t textinput.Model
	for i := range m.inputs {
		t = textinput.New()
		t.Cursor.Style = cursorStyle
		t.CharLimit = 128

		switch i {
		case 0:
			t.Placeholder = "JIRA URL (e.g., https://company.atlassian.net)"
			t.SetValue(cfg.Integrations.Jira.URL)
			t.Focus()
		case 1:
			t.Placeholder = "JIRA Email"
			t.SetValue(cfg.Integrations.Jira.Email)
		case 2:
			t.Placeholder = "JIRA API Token"
			t.SetValue(cfg.Integrations.Jira.APIToken)
			t.EchoMode = textinput.EchoPassword
			t.EchoCharacter = '•'
		}

		m.inputs[i] = t
	}

	return m
}

func (m SetupModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m SetupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit

		case "tab", "shift+tab", "enter", "up", "down":
			s := msg.String()

			if s == "enter" && m.focusIndex == len(m.inputs) {
				return m.save()
			}

			if s == "up" || s == "shift+tab" {
				m.focusIndex--
			} else {
				m.focusIndex++
			}

			if m.focusIndex > len(m.inputs) {
				m.focusIndex = 0
			} else if m.focusIndex < 0 {
				m.focusIndex = len(m.inputs)
			}

			cmds := make([]tea.Cmd, len(m.inputs))
			for i := 0; i <= len(m.inputs)-1; i++ {
				if i == m.focusIndex {
					cmds[i] = m.inputs[i].Focus()
					continue
				}
				m.inputs[i].Blur()
			}

			return m, tea.Batch(cmds...)
		}
	}

	cmd := m.updateInputs(msg)
	return m, cmd
}

func (m *SetupModel) updateInputs(msg tea.Msg) tea.Cmd {
	cmds := make([]tea.Cmd, len(m.inputs))

	for i := range m.inputs {
		m.inputs[i], cmds[i] = m.inputs[i].Update(msg)
	}

	return tea.Batch(cmds...)
}

func (m SetupModel) save() (tea.Model, tea.Cmd) {
	m.cfg.Integrations.Jira.URL = m.inputs[0].Value()
	m.cfg.Integrations.Jira.Email = m.inputs[1].Value()
	m.cfg.Integrations.Jira.APIToken = m.inputs[2].Value()

	if err := m.cfg.Save(); err != nil {
		m.err = err
		return m, nil
	}

	m.saved = true
	return m, nil
}

func (m SetupModel) View() string {
	if m.saved {
		return "Configuration saved! Please restart Aiman.\n"
	}

	var b strings.Builder
	b.WriteString("Aiman Setup - JIRA Configuration\n\n")

	for i := range m.inputs {
		b.WriteString(m.inputs[i].View())
		b.WriteString("\n")
	}

	button := &strings.Builder{}
	fmt.Fprintf(button, "[ Save ]")
	if m.focusIndex == len(m.inputs) {
		b.WriteString("\n" + activeStyle.Render(button.String()) + "\n")
	} else {
		b.WriteString("\n" + button.String() + "\n")
	}

	b.WriteString("\n(esc to quit, tab to navigate)\n")

	return docStyle.Render(b.String())
}

var (
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
)
