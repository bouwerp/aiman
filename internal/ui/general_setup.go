package ui

import (
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/infra/config"
	tea "github.com/charmbracelet/bubbletea"
)

type GeneralSetupModel struct {
	cfg         *config.Config
	inputDetect bool
	focusIndex  int
	saved       bool
	err         error
}

func NewGeneralSetupModel(cfg *config.Config) GeneralSetupModel {
	return GeneralSetupModel{
		cfg:         cfg,
		inputDetect: cfg.Features.InputPromptDetection,
	}
}

func (m GeneralSetupModel) Init() tea.Cmd {
	return nil
}

func (m GeneralSetupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			return m, nil
		case "up", "shift+tab":
			m.focusIndex--
			if m.focusIndex < 0 {
				m.focusIndex = 1
			}
			return m, nil
		case "down", "tab":
			m.focusIndex++
			if m.focusIndex > 1 {
				m.focusIndex = 0
			}
			return m, nil
		case "enter":
			if m.focusIndex == 0 {
				m.inputDetect = !m.inputDetect
				return m, nil
			}
			if m.focusIndex == 1 {
				return m.save()
			}
		}
	}

	return m, nil
}

func (m GeneralSetupModel) save() (tea.Model, tea.Cmd) {
	m.cfg.Features.InputPromptDetection = m.inputDetect
	if err := m.cfg.Save(); err != nil {
		m.err = err
		return m, nil
	}
	m.saved = true
	return m, nil
}

func (m GeneralSetupModel) View() string {
	if m.saved {
		return "General settings saved!\n"
	}

	var b strings.Builder
	b.WriteString("General Settings\n")
	b.WriteString("Configure experimental and general features\n\n")

	status := "[ ]"
	if m.inputDetect {
		status = "[✓]"
	}
	label := fmt.Sprintf("%s Experimental: Detect input prompts", status)
	if m.focusIndex == 0 {
		label = activeStyle.Render(label)
	}
	b.WriteString(label + "\n\n")

	saveLabel := "[ Save ]"
	if m.focusIndex == 1 {
		saveLabel = activeStyle.Render(saveLabel)
	}
	b.WriteString(saveLabel + "\n")

	if m.err != nil {
		b.WriteString("\n" + failStyle.Render(fmt.Sprintf("Error: %v", m.err)) + "\n")
	}

	b.WriteString("\n(tab/shift+tab to navigate, enter to toggle/save, esc to cancel)\n")

	return docStyle.Render(b.String())
}
