package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

type agentItem struct {
	agent domain.Agent
}

func (i agentItem) Title() string       { return i.agent.Name }
func (i agentItem) Description() string { return i.agent.Description }
func (i agentItem) FilterValue() string { return i.agent.Name }

// tierItem is a picker row that leaves the agent to serve's tier pick, so a
// new session can choose smart or worker instead of one binary.
type tierItem struct {
	tier  string
	label string
}

func (i tierItem) Title() string       { return i.label }
func (i tierItem) Description() string { return tierUsedFor(i.tier) }
func (i tierItem) FilterValue() string { return i.label }

type AgentPickerModel struct {
	list     list.Model
	selected *domain.Agent
	// selectedTier is the tier a tier row chose; empty when an agent was
	// picked. The create wizard sends it to serve.
	selectedTier string
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

// Preselect moves the cursor to the agent whose command's binary is command
// (case-insensitive). It returns false and leaves the cursor in place when no
// listed agent matches, so a role agent that is not installed has no effect.
func (m *AgentPickerModel) Preselect(command string) bool {
	want := strings.Fields(strings.ToLower(command))
	if len(want) == 0 {
		return false
	}
	for i, it := range m.list.Items() {
		ai, ok := it.(agentItem)
		if !ok {
			continue
		}
		fields := strings.Fields(strings.ToLower(ai.agent.Command))
		if len(fields) > 0 && fields[0] == want[0] {
			m.list.Select(i)
			return true
		}
	}
	return false
}

// preferredAgentChoice is the picker's answer when the user confirms without
// an agent: no command, so serve picks from the model tiers.
func preferredAgentChoice() *domain.Agent { return preferredTierChoice("") }

// preferredTierChoice is the answer for a tier row. It carries no command:
// serve resolves the installed entry of that tier.
func preferredTierChoice(tier string) *domain.Agent {
	switch tier {
	case config.TierSmart:
		return &domain.Agent{Name: "Smart tier"}
	case config.TierWorker:
		return &domain.Agent{Name: "Worker tier"}
	}
	return &domain.Agent{Name: "First installed from agents.preferred"}
}

// EnableTiers puts the two tier rows above the agents: the create wizard
// offers a tier instead of one binary. The cursor starts on smart when that
// tier has entries, otherwise on worker.
func (m *AgentPickerModel) EnableTiers(agents config.AgentsConfig) {
	rows := make([]list.Item, 0, len(m.list.Items())+2)
	for _, tier := range []string{config.TierSmart, config.TierWorker} {
		rows = append(rows, tierItem{tier: tier, label: tierRowLabel(agents, tier)})
	}
	rows = append(rows, m.list.Items()...)
	m.list.SetItems(rows)
	if len(agents.Tier(config.TierSmart)) > 0 {
		m.list.Select(0)
		return
	}
	m.list.Select(1)
}

// tierRowLabel is "Smart tier: <first entry>", or a hint that the tier is
// empty and falls back.
func tierRowLabel(agents config.AgentsConfig, tier string) string {
	name := strings.ToUpper(tier[:1]) + tier[1:] + " tier"
	entries := agents.Tier(tier)
	if len(entries) == 0 {
		return fmt.Sprintf("%s: empty (%s)", name, tierEmptyHint(tier))
	}
	return name + ": " + entries[0].Label()
}

// isPreferredAgentChoice reports whether the choice leaves the agent to serve.
func isPreferredAgentChoice(a *domain.Agent) bool {
	return a != nil && strings.TrimSpace(a.Command) == ""
}

func (m AgentPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		if msg.String() == "enter" {
			switch i := m.list.SelectedItem().(type) {
			case tierItem:
				m.selectedTier = i.tier
				m.selected = preferredTierChoice(i.tier)
				return m, nil
			case agentItem:
				m.selectedTier = ""
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
