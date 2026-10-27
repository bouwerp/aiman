package ui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/bouwerp/aiman/internal/domain"
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
	// allowPreferred lets enter with no agents listed confirm the
	// agents.preferred choice. Only a new session sets it; a restart needs a
	// real agent.
	allowPreferred bool
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

// preferredAgentChoice is the picker's answer when the user confirms without
// an agent: no command, so serve launches the first agents.preferred command
// installed on the remote.
func preferredAgentChoice() *domain.Agent {
	return &domain.Agent{Name: "First installed from agents.preferred"}
}

// isPreferredAgentChoice reports whether the choice leaves the agent to serve.
func isPreferredAgentChoice(a *domain.Agent) bool {
	return a != nil && strings.TrimSpace(a.Command) == ""
}

func (m AgentPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		if msg.String() == "enter" {
			if i, ok := m.list.SelectedItem().(agentItem); ok {
				m.selected = &i.agent
				return m, nil
			}
			if m.allowPreferred && len(m.list.Items()) == 0 {
				m.selected = preferredAgentChoice()
				return m, nil
			}
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m AgentPickerModel) viewString() string {
	if len(m.list.Items()) == 0 {
		msg := "\n  No AI agents found on the remote server.\n  Please install at least one of: Claude Code, Antigravity CLI, Cursor, Kilo Code, or GitHub Copilot CLI.\n\n"
		if m.allowPreferred {
			return msg + "  Press enter to let the agent API start the first installed agent from agents.preferred, or ESC to go back."
		}
		return msg + "  Press ESC to go back."
	}
	return m.list.View()
}

func (m AgentPickerModel) View() tea.View {
	return newView(m.viewString())
}
