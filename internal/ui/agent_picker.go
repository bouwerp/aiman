package ui

import (
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

type agentItem struct {
	agent domain.Agent
}

func (i agentItem) Title() string       { return i.agent.Name }
func (i agentItem) Description() string { return i.agent.Description }
func (i agentItem) FilterValue() string { return i.agent.Name }

type AgentPickerModel struct {
	list     list.Model
	selected *domain.Agent
}

func NewAgentPickerModel(agents []domain.Agent) AgentPickerModel {
	items := make([]list.Item, len(agents))
	for i, a := range agents {
		items[i] = agentItem{agent: a}
	}

	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Select AI Agent"

	// Enable VSCode-style search/filter
	l.SetShowFilter(true)
	l.FilterInput.Placeholder = "Type to filter agents..."
	l.FilterInput.Focus()

	// Disable the default "n/p" bindings so they don't interfere with typing
	l.KeyMap.NextPage = key.Binding{}
	l.KeyMap.PrevPage = key.Binding{}

	return AgentPickerModel{
		list: l,
	}
}

func (m AgentPickerModel) Init() tea.Cmd {
	return nil
}

func (m *AgentPickerModel) SetSize(width, height int) {
	h, v := docStyle.GetFrameSize()
	m.list.SetSize(width-h, height-v)
}

func (m AgentPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		if msg.String() == "enter" {
			if i, ok := m.list.SelectedItem().(agentItem); ok {
				m.selected = &i.agent
				return m, nil
			}
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m AgentPickerModel) View() string {
	if len(m.list.Items()) == 0 {
		return "\n  No AI agents found on the remote server.\n  Please install at least one of: Claude Code, Gemini CLI, OpenCode, or GitHub Copilot CLI.\n\n  Press ESC to go back."
	}
	return m.list.View()
}
