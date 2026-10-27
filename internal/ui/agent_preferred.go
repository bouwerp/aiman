package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/bouwerp/aiman/internal/infra/config"
)

// AgentPreferredModel edits the ordered list a coordinator uses when it starts
// child sessions.
type AgentPreferredModel struct {
	cfg    *config.Config
	items  []string
	cursor int
	adding bool
	input  string
	saved  bool
	err    error
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
		m.adding = true
		m.input = ""
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

func (m AgentPreferredModel) updateAdding(km tea.KeyPressMsg) AgentPreferredModel {
	switch km.String() {
	case "esc":
		m.adding, m.input = false, ""
	case "enter":
		name := strings.TrimSpace(m.input)
		if name != "" && !containsString(m.items, name) {
			m.items = append(m.items, name)
			m.cursor = len(m.items) - 1
		}
		m.adding, m.input = false, ""
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
		b.WriteString("\n  Add agent: " + m.input + "\n")
	}
	if m.err != nil {
		b.WriteString("\n" + failStyle.Render(fmt.Sprintf("Error: %v", m.err)) + "\n")
	}
	b.WriteString("\n(j/k or arrows move, J/K reorder, a add, d delete, enter save, esc cancel)\n")
	return docStyle.Render(b.String())
}

func (m AgentPreferredModel) View() tea.View {
	return newView(m.viewString())
}
