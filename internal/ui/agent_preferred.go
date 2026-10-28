package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// preferredChoice is one supported agent the add picker offers.
type preferredChoice struct {
	name    string
	command string
}

// AgentPreferredModel edits the ordered list a coordinator uses when it starts
// child sessions.
type AgentPreferredModel struct {
	cfg    *config.Config
	items  []string
	cursor int
	adding bool
	// choices are the supported agents not yet in items; pick indexes them,
	// and len(choices) is the row that switches to typing a command.
	choices []preferredChoice
	pick    int
	typing  bool
	input   string
	saved   bool
	err     error
}

func NewAgentPreferredModel(cfg *config.Config) AgentPreferredModel {
	m := AgentPreferredModel{cfg: cfg}
	if cfg != nil {
		m.items = append([]string{}, cfg.Agents.Preferred...)
	}
	return m
}

func (m AgentPreferredModel) Init() tea.Cmd { return nil }

func (m AgentPreferredModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	if m.adding {
		return m.updateAdding(km), nil
	}
	switch km.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		return m, nil
	case "j", "down":
		if m.cursor < len(m.items)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "J":
		m.move(-1)
	case "K":
		m.move(1)
	case "a":
		m.adding, m.typing, m.input, m.pick = true, false, "", 0
		m.choices = m.availableChoices()
	case "d":
		m.remove()
	case "enter":
		return m.save()
	}
	return m, nil
}

func (m *AgentPreferredModel) move(delta int) {
	to := m.cursor + delta
	if m.cursor < 0 || m.cursor >= len(m.items) || to < 0 || to >= len(m.items) {
		return
	}
	m.items[m.cursor], m.items[to] = m.items[to], m.items[m.cursor]
	m.cursor = to
}

func (m *AgentPreferredModel) remove() {
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return
	}
	m.items = append(m.items[:m.cursor], m.items[m.cursor+1:]...)
	if m.cursor >= len(m.items) && m.cursor > 0 {
		m.cursor--
	}
}

// availableChoices lists each supported agent command once, skipping those
// already preferred.
func (m AgentPreferredModel) availableChoices() []preferredChoice {
	var out []preferredChoice
	seen := map[string]bool{}
	for _, a := range agent.KnownAgents() {
		cmd := agent.CommandBase(a.Command)
		if cmd == "" || seen[cmd] || containsString(m.items, cmd) {
			continue
		}
		seen[cmd] = true
		out = append(out, preferredChoice{name: a.Name, command: cmd})
	}
	return out
}

func (m *AgentPreferredModel) appendItem(name string) {
	name = strings.TrimSpace(name)
	if name != "" && !containsString(m.items, name) {
		m.items = append(m.items, name)
		m.cursor = len(m.items) - 1
	}
}

func (m AgentPreferredModel) updateAdding(km tea.KeyPressMsg) AgentPreferredModel {
	if m.typing {
		return m.updateTyping(km)
	}
	switch km.String() {
	case "esc":
		m.adding = false
	case "j", "down":
		if m.pick < len(m.choices) {
			m.pick++
		}
	case "k", "up":
		if m.pick > 0 {
			m.pick--
		}
	case "enter":
		if m.pick == len(m.choices) {
			m.typing, m.input = true, ""
			return m
		}
		m.appendItem(m.choices[m.pick].command)
		m.adding = false
	}
	return m
}

func (m AgentPreferredModel) updateTyping(km tea.KeyPressMsg) AgentPreferredModel {
	switch km.String() {
	case "esc":
		m.typing, m.input = false, ""
	case "enter":
		m.appendItem(m.input)
		m.adding, m.typing, m.input = false, false, ""
	case "backspace":
		if m.input != "" {
			m.input = m.input[:len(m.input)-1]
		}
	default:
		if km.Text != "" {
			m.input += km.Text
		}
	}
	return m
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func (m AgentPreferredModel) save() (tea.Model, tea.Cmd) {
	if m.cfg == nil {
		m.err = fmt.Errorf("config is not loaded")
		return m, nil
	}
	m.cfg.Agents.Preferred = append([]string{}, m.items...)
	if err := m.cfg.Save(); err != nil {
		m.err = err
		return m, nil
	}
	m.saved = true
	return m, nil
}

func (m AgentPreferredModel) viewString() string {
	if m.saved {
		return "Preferred agents saved!\n"
	}
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	var b strings.Builder
	b.WriteString("Preferred agents\n")
	b.WriteString("Order of agents a coordinator may start. The server picks the first installed one.\n\n")
	if len(m.items) == 0 {
		b.WriteString(dim.Render("  (none — a child create will fail until one is added)") + "\n")
	}
	for i, name := range m.items {
		row := fmt.Sprintf("%2d. %s", i+1, name)
		if i == m.cursor && !m.adding {
			b.WriteString(activeStyle.Render(row) + "\n")
		} else {
			b.WriteString("  " + row + "\n")
		}
	}
	if m.adding {
		b.WriteString("\n" + m.addView(dim))
	}
	if m.err != nil {
		b.WriteString("\n" + failStyle.Render(fmt.Sprintf("Error: %v", m.err)) + "\n")
	}
	help := "(j/k or arrows move, J/K reorder, a add, d delete, enter save, esc cancel)"
	if m.adding {
		help = "(j/k or arrows choose, enter add, esc back)"
	}
	b.WriteString("\n" + help + "\n")
	return docStyle.Render(b.String())
}

func (m AgentPreferredModel) addView(dim lipgloss.Style) string {
	if m.typing {
		return "  Agent command: " + m.input + "\n"
	}
	var b strings.Builder
	b.WriteString("  Add a supported agent:\n")
	for i, c := range m.choices {
		row := fmt.Sprintf("%-16s %s", c.command, dim.Render(c.name))
		if i == m.pick {
			b.WriteString("  " + activeStyle.Render("> "+row) + "\n")
		} else {
			b.WriteString("    " + row + "\n")
		}
	}
	other := "Other (type a command)"
	if m.pick == len(m.choices) {
		b.WriteString("  " + activeStyle.Render("> "+other) + "\n")
	} else {
		b.WriteString("    " + other + "\n")
	}
	return b.String()
}

func (m AgentPreferredModel) View() tea.View {
	return newView(m.viewString())
}
