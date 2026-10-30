package ui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/infra/config"
)

type agentItem struct {
	agent        domain.Agent
	defaultModel string
}

func (i agentItem) Title() string { return i.agent.Name }
func (i agentItem) Description() string {
	if i.defaultModel == "" {
		return i.agent.Description
	}
	if i.agent.Description == "" {
		return i.defaultModel
	}
	return i.agent.Description + "  [" + i.defaultModel + "]"
}
func (i agentItem) FilterValue() string { return i.agent.Name }

type agentPickStep int

const (
	agentPickList agentPickStep = iota
	agentPickModelLoading
	agentPickProvider
	agentPickModel
	agentPickModelTyping
)

type AgentPickerModel struct {
	list     list.Model
	selected *domain.Agent
	// allowPreferred lets enter with no agents listed confirm the
	// agents.preferred choice. Only a new session sets it; a restart needs a
	// real agent.
	allowPreferred bool

	host     string
	lister   modelLister
	defaults map[string]string

	step     agentPickStep
	pending  *domain.Agent
	models   []agent.ModelOption
	dynamic  bool
	provider string
	filter   string
	pick     int
	input    string
	loadErr  error
}

func NewAgentPickerModel(agents []domain.Agent) AgentPickerModel {
	items := make([]list.Item, len(agents))
	for i, a := range agents {
		items[i] = agentItem{agent: a, defaultModel: agentListDefault(nil, a.Command)}
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

// SetModelSource records where models are listed from and refreshes each
// row's default-model label from defaults (config agent_defaults) or the
// catalog.
func (m *AgentPickerModel) SetModelSource(host string, lister modelLister, defaults map[string]string) {
	m.host, m.lister, m.defaults = host, lister, defaults
	items := m.list.Items()
	for i, it := range items {
		ai, ok := it.(agentItem)
		if !ok {
			continue
		}
		ai.defaultModel = agentListDefault(defaults, ai.agent.Command)
		items[i] = ai
	}
	m.list.SetItems(items)
}

func agentListDefault(defaults map[string]string, command string) string {
	base := agent.CommandBase(command)
	if m := strings.TrimSpace(defaults[base]); m != "" {
		return m
	}
	if agent.HasDynamicModels(command) {
		return "host default"
	}
	cat := agent.LaunchCatalogFor(base)
	if len(cat.Models) > 0 {
		return cat.Models[0]
	}
	return ""
}

func agentDefaultModels(cfg *config.Config) map[string]string {
	if cfg == nil || cfg.AgentDefaults == nil {
		return nil
	}
	out := map[string]string{}
	for k, d := range cfg.AgentDefaults {
		if m := strings.TrimSpace(d.Model); m != "" {
			out[k] = m
		}
	}
	return out
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
func preferredAgentChoice() *domain.Agent {
	return &domain.Agent{Name: "First installed from agents.preferred"}
}

// isPreferredAgentChoice reports whether the choice leaves the agent to serve.
func isPreferredAgentChoice(a *domain.Agent) bool {
	return a != nil && strings.TrimSpace(a.Command) == ""
}

func (m AgentPickerModel) inModelFlow() bool {
	return m.step != agentPickList
}

func (m AgentPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if lm, ok := msg.(preferredModelsMsg); ok {
		return m.applyModels(lm), nil
	}
	if m.inModelFlow() {
		km, ok := msg.(tea.KeyPressMsg)
		if !ok {
			return m, nil
		}
		return m.updateModelPick(km)
	}
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		if msg.String() == "enter" {
			if i, ok := m.list.SelectedItem().(agentItem); ok {
				return m.chooseAgent(i.agent)
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

func (m AgentPickerModel) chooseAgent(a domain.Agent) (tea.Model, tea.Cmd) {
	if !needsModelPick(a.Command) {
		return m.confirm(a, ""), nil
	}
	return m.startModelPick(a)
}

func (m AgentPickerModel) viewString() string {
	if m.inModelFlow() {
		return m.viewModelPick()
	}
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
