package ui

import (
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type agentDefaultRow struct {
	key  string
	name string
}

type AgentDefaultsModel struct {
	cfg        *config.Config
	rows       []agentDefaultRow
	models     []textinput.Model
	efforts    []textinput.Model
	focusIndex int
	saved      bool
	err        error
}

func NewAgentDefaultsModel(cfg *config.Config) AgentDefaultsModel {
	known := agent.KnownAgents()
	rows := make([]agentDefaultRow, 0, len(known))
	seen := map[string]bool{}
	for _, a := range known {
		fields := strings.Fields(strings.ToLower(a.Command))
		if len(fields) == 0 {
			continue
		}
		key := fields[0]
		if seen[key] {
			continue
		}
		seen[key] = true
		rows = append(rows, agentDefaultRow{key: key, name: a.Name})
	}
	models := make([]textinput.Model, len(rows))
	efforts := make([]textinput.Model, len(rows))
	for i, r := range rows {
		d := config.AgentDefaults{}
		if cfg != nil && cfg.AgentDefaults != nil {
			d = cfg.AgentDefaults[r.key]
		}
		models[i] = newAgentDefaultInput("model", d.Model)
		efforts[i] = newAgentDefaultInput("effort", d.Effort)
	}
	if len(models) > 0 {
		models[0].Focus()
	}
	return AgentDefaultsModel{cfg: cfg, rows: rows, models: models, efforts: efforts}
}

func newAgentDefaultInput(placeholder, value string) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.SetValue(value)
	ti.CharLimit = 64
	ti.Width = 22
	return ti
}

func (m AgentDefaultsModel) Init() tea.Cmd { return nil }

func (m AgentDefaultsModel) fieldCount() int { return len(m.rows)*2 + 1 }

func (m AgentDefaultsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			return m, nil
		case "tab", "down":
			m.focusIndex++
			if m.focusIndex >= m.fieldCount() {
				m.focusIndex = 0
			}
			m.syncFocus()
			return m, nil
		case "shift+tab", "up":
			m.focusIndex--
			if m.focusIndex < 0 {
				m.focusIndex = m.fieldCount() - 1
			}
			m.syncFocus()
			return m, nil
		case "enter":
			if m.focusIndex == m.fieldCount()-1 {
				return m.save()
			}
		}
	}
	i := m.focusIndex
	if i < len(m.rows)*2 {
		row := i / 2
		var cmd tea.Cmd
		if i%2 == 0 {
			m.models[row], cmd = m.models[row].Update(msg)
		} else {
			m.efforts[row], cmd = m.efforts[row].Update(msg)
		}
		return m, cmd
	}
	return m, nil
}

func (m *AgentDefaultsModel) syncFocus() {
	save := m.focusIndex == m.fieldCount()-1
	for i := range m.rows {
		if !save && m.focusIndex/2 == i && m.focusIndex%2 == 0 {
			m.models[i].Focus()
		} else {
			m.models[i].Blur()
		}
		if !save && m.focusIndex/2 == i && m.focusIndex%2 == 1 {
			m.efforts[i].Focus()
		} else {
			m.efforts[i].Blur()
		}
	}
}

func (m AgentDefaultsModel) save() (tea.Model, tea.Cmd) {
	if m.cfg.AgentDefaults == nil {
		m.cfg.AgentDefaults = map[string]config.AgentDefaults{}
	}
	for i, r := range m.rows {
		d := config.AgentDefaults{
			Model:  strings.TrimSpace(m.models[i].Value()),
			Effort: strings.TrimSpace(m.efforts[i].Value()),
		}
		if d.Model == "" && d.Effort == "" {
			delete(m.cfg.AgentDefaults, r.key)
			continue
		}
		m.cfg.AgentDefaults[r.key] = d
	}
	if err := m.cfg.Save(); err != nil {
		m.err = err
		return m, nil
	}
	m.saved = true
	return m, nil
}

func (m AgentDefaultsModel) View() string {
	if m.saved {
		return "Agent defaults saved!\n"
	}
	var b strings.Builder
	b.WriteString("Agent defaults\n")
	b.WriteString("Launch model and thinking/reasoning effort per agent. Empty = agent default.\n")
	b.WriteString("Claude: --model / --effort. Grok: --model / --reasoning-effort. Codex: --model / -c model_reasoning_effort.\n\n")
	b.WriteString(fmt.Sprintf("  %-22s  %-24s  %s\n", "Agent", "Model", "Effort"))
	for i, r := range m.rows {
		name := fmt.Sprintf("%-22s", r.name)
		if m.focusIndex/2 == i && m.focusIndex < len(m.rows)*2 {
			name = activeStyle.Render(name)
		}
		fmt.Fprintf(&b, "  %s  %s  %s\n", name, m.models[i].View(), m.efforts[i].View())
	}
	saveLabel := "[ Save ]"
	if m.focusIndex == m.fieldCount()-1 {
		saveLabel = activeStyle.Render(saveLabel)
	}
	b.WriteString("\n" + saveLabel + "\n")
	if m.err != nil {
		b.WriteString("\n" + failStyle.Render(fmt.Sprintf("Error: %v", m.err)) + "\n")
	}
	b.WriteString("\n(tab to move, enter to save, esc to cancel)\n")
	return docStyle.Render(b.String())
}
