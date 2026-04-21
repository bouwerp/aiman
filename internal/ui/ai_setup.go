package ui

import (
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// AISetupModel is the settings screen for configuring the local AI backend.
// It mirrors the pattern of GeneralSetupModel and SetupModel.
type AISetupModel struct {
	cfg        *config.Config
	enabled    bool
	hostInput  textinput.Model
	modelInput textinput.Model
	focusIndex int // 0=enabled toggle, 1=host, 2=model, 3=save
	saved      bool
	err        error
}

const (
	aiSetupFocusEnabled = 0
	aiSetupFocusHost    = 1
	aiSetupFocusModel   = 2
	aiSetupFocusSave    = 3
	aiSetupFocusMax     = 3
)

func NewAISetupModel(cfg *config.Config) AISetupModel {
	host := textinput.New()
	host.Cursor.Style = cursorStyle
	host.CharLimit = 256
	host.Placeholder = "http://localhost:11434"
	host.SetValue(cfg.AI.OllamaHost)

	model := textinput.New()
	model.Cursor.Style = cursorStyle
	model.CharLimit = 128
	model.Placeholder = "qwen3:4b"
	model.SetValue(cfg.AI.Model)

	return AISetupModel{
		cfg:        cfg,
		enabled:    cfg.AI.Enabled,
		hostInput:  host,
		modelInput: model,
	}
}

func (m AISetupModel) Init() tea.Cmd {
	return nil
}

func (m AISetupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			return m, nil
		case "up", "shift+tab":
			m.focusIndex--
			if m.focusIndex < 0 {
				m.focusIndex = aiSetupFocusMax
			}
			return m, m.syncFocus()
		case "down", "tab":
			m.focusIndex++
			if m.focusIndex > aiSetupFocusMax {
				m.focusIndex = 0
			}
			return m, m.syncFocus()
		case "enter", " ":
			switch m.focusIndex {
			case aiSetupFocusEnabled:
				m.enabled = !m.enabled
				return m, nil
			case aiSetupFocusSave:
				return m.save()
			}
		}
	}

	var cmd tea.Cmd
	switch m.focusIndex {
	case aiSetupFocusHost:
		m.hostInput, cmd = m.hostInput.Update(msg)
	case aiSetupFocusModel:
		m.modelInput, cmd = m.modelInput.Update(msg)
	}
	return m, cmd
}

func (m *AISetupModel) syncFocus() tea.Cmd {
	m.hostInput.Blur()
	m.modelInput.Blur()
	switch m.focusIndex {
	case aiSetupFocusHost:
		return m.hostInput.Focus()
	case aiSetupFocusModel:
		return m.modelInput.Focus()
	}
	return nil
}

func (m AISetupModel) save() (tea.Model, tea.Cmd) {
	m.cfg.AI.Enabled = m.enabled
	m.cfg.AI.OllamaHost = m.hostInput.Value()
	m.cfg.AI.Model = m.modelInput.Value()
	if err := m.cfg.Save(); err != nil {
		m.err = err
		return m, nil
	}
	m.saved = true
	return m, nil
}

func (m AISetupModel) View() string {
	if m.saved {
		return "AI settings saved! Restart Aiman or press 'i' to try it now.\n"
	}

	var b strings.Builder
	b.WriteString("AI Settings\n")
	b.WriteString("Configure the local AI backend (Ollama)\n\n")

	// Enabled toggle
	check := "[ ]"
	if m.enabled {
		check = "[✓]"
	}
	label := fmt.Sprintf("%s Enable local AI (requires Ollama running)", check)
	if m.focusIndex == aiSetupFocusEnabled {
		label = activeStyle.Render(label)
	}
	b.WriteString(label + "\n\n")

	// Host input
	hostLabel := "Ollama host"
	if m.focusIndex == aiSetupFocusHost {
		hostLabel = activeStyle.Render(hostLabel)
	}
	b.WriteString(hostLabel + "\n")
	b.WriteString(m.hostInput.View() + "\n\n")

	// Model input
	modelLabel := "Model name"
	if m.focusIndex == aiSetupFocusModel {
		modelLabel = activeStyle.Render(modelLabel)
	}
	b.WriteString(modelLabel + "\n")
	b.WriteString(m.modelInput.View() + "\n\n")

	// Save button
	saveLabel := "[ Save ]"
	if m.focusIndex == aiSetupFocusSave {
		saveLabel = activeStyle.Render(saveLabel)
	}
	b.WriteString(saveLabel + "\n")

	if m.err != nil {
		b.WriteString("\n" + failStyle.Render(fmt.Sprintf("Error: %v", m.err)) + "\n")
	}

	b.WriteString("\n(tab/shift+tab to navigate, enter/space to toggle, esc to cancel)\n")

	return docStyle.Render(b.String())
}
