package ui

import (
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type EC2SetupModel struct {
	cfg        *config.Config
	inputs     []textinput.Model
	focusIndex int
	err        error
	saved      bool
}

func NewEC2SetupModel(cfg *config.Config) EC2SetupModel {
	m := EC2SetupModel{
		cfg:    cfg,
		inputs: make([]textinput.Model, 6),
	}

	for i := range m.inputs {
		t := textinput.New()
		t.CharLimit = 128
		t.PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
		m.inputs[i] = t
	}

	m.inputs[0].Placeholder = "AWS Profile (e.g. default)"
	m.inputs[0].SetValue(cfg.EC2Loop.DefaultProfile)
	m.inputs[0].Focus()

	m.inputs[1].Placeholder = "AWS Region (e.g. us-east-1)"
	m.inputs[1].SetValue(cfg.EC2Loop.DefaultRegion)

	m.inputs[2].Placeholder = "Instance Type (e.g. t3.large)"
	m.inputs[2].SetValue(cfg.EC2Loop.DefaultInstanceType)

	m.inputs[3].Placeholder = "Subnet ID (optional)"
	m.inputs[3].SetValue(cfg.EC2Loop.DefaultSubnetID)

	m.inputs[4].Placeholder = "Security Group ID (optional)"
	m.inputs[4].SetValue(cfg.EC2Loop.DefaultSecurityGroup)

	m.inputs[5].Placeholder = "SSH Key Name (optional)"
	m.inputs[5].SetValue(cfg.EC2Loop.DefaultKeyName)

	return m
}

func (m EC2SetupModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m EC2SetupModel) Update(msg tea.Msg) (EC2SetupModel, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "esc":
			return m, nil

		case "enter", "up", "down", "tab", "shift+tab":
			s := msg.String()

			if s == "enter" && m.focusIndex == len(m.inputs) {
				m.cfg.EC2Loop.DefaultProfile = strings.TrimSpace(m.inputs[0].Value())
				m.cfg.EC2Loop.DefaultRegion = strings.TrimSpace(m.inputs[1].Value())
				m.cfg.EC2Loop.DefaultInstanceType = strings.TrimSpace(m.inputs[2].Value())
				m.cfg.EC2Loop.DefaultSubnetID = strings.TrimSpace(m.inputs[3].Value())
				m.cfg.EC2Loop.DefaultSecurityGroup = strings.TrimSpace(m.inputs[4].Value())
				m.cfg.EC2Loop.DefaultKeyName = strings.TrimSpace(m.inputs[5].Value())

				m.err = m.cfg.Save()
				if m.err == nil {
					m.saved = true
				}
				return m, nil
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
					m.inputs[i].PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
				} else {
					m.inputs[i].Blur()
					m.inputs[i].PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
				}
			}
			return m, tea.Batch(cmds...)
		}
	}

	cmd := m.updateInputs(msg)
	return m, cmd
}

func (m *EC2SetupModel) updateInputs(msg tea.Msg) tea.Cmd {
	cmds := make([]tea.Cmd, len(m.inputs))
	for i := range m.inputs {
		m.inputs[i], cmds[i] = m.inputs[i].Update(msg)
	}
	return tea.Batch(cmds...)
}

func (m EC2SetupModel) View() string {
	var b strings.Builder
	b.WriteString("\n  EC2 Autonomous Loop Settings\n\n")

	labels := []string{
		"AWS Profile",
		"AWS Region",
		"Instance Type",
		"Subnet ID",
		"Security Group ID",
		"SSH Key Name",
	}

	for i := range m.inputs {
		b.WriteString(fmt.Sprintf("  %-20s: %s\n", labels[i], m.inputs[i].View()))
	}

	button := "  [ Save ]"
	if m.focusIndex == len(m.inputs) {
		button = lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Render(button)
	}
	b.WriteString(fmt.Sprintf("\n%s\n", button))

	if m.saved {
		b.WriteString("\n  Settings saved successfully. Press ESC to go back.\n")
	}
	if m.err != nil {
		b.WriteString(fmt.Sprintf("\n  Error saving: %s\n", m.err.Error()))
	}

	return b.String()
}
