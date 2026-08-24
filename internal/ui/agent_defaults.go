package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/infra/config"
)

type agentDefaultRow struct {
	key       string
	name      string
	models    []string
	efforts   []string
	modelIdx  int
	effortIdx int
	hasEffort bool
}

type AgentDefaultsModel struct {
	cfg        *config.Config
	rows       []agentDefaultRow
	focusIndex int
	saved      bool
	err        error
}

func NewAgentDefaultsModel(cfg *config.Config) AgentDefaultsModel {
	known := agent.KnownAgents()
	rows := make([]agentDefaultRow, 0, len(known))
	seen := map[string]bool{}
	for _, a := range known {
		key := agent.CommandBase(a.Command)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		cat := agent.LaunchCatalogFor(key)
		d := config.AgentDefaults{}
		if cfg != nil && cfg.AgentDefaults != nil {
			d = cfg.AgentDefaults[key]
		}
		models := withEmptyOption(cat.Models, d.Model)
		row := agentDefaultRow{
			key:      key,
			name:     a.Name,
			models:   models,
			modelIdx: indexOf(models, strings.TrimSpace(d.Model)),
		}
		if cat.SupportsEffort() {
			efforts := withEmptyOption(cat.Efforts, d.Effort)
			row.hasEffort = true
			row.efforts = efforts
			row.effortIdx = indexOf(efforts, strings.TrimSpace(d.Effort))
		}
		rows = append(rows, row)
	}
	return AgentDefaultsModel{cfg: cfg, rows: rows}
}

func withEmptyOption(values []string, extra string) []string {
	out := make([]string, 0, len(values)+2)
	out = append(out, "")
	seen := map[string]bool{"": true}
	for _, v := range values {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	extra = strings.TrimSpace(extra)
	if extra != "" && !seen[extra] {
		out = append(out, extra)
	}
	return out
}

func indexOf(values []string, want string) int {
	for i, v := range values {
		if v == want {
			return i
		}
	}
	return 0
}

func (m AgentDefaultsModel) Init() tea.Cmd { return nil }

func (m AgentDefaultsModel) fieldCount() int {
	n := 1
	for _, r := range m.rows {
		n++
		if r.hasEffort {
			n++
		}
	}
	return n
}

func (m AgentDefaultsModel) focusSave() bool {
	return m.focusIndex == m.fieldCount()-1
}

// focusCell returns the row and whether the effort column is focused.
func (m AgentDefaultsModel) focusCell() (row int, effort bool) {
	n := 0
	for i, r := range m.rows {
		if n == m.focusIndex {
			return i, false
		}
		n++
		if r.hasEffort {
			if n == m.focusIndex {
				return i, true
			}
			n++
		}
	}
	return 0, false
}

func (m AgentDefaultsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
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
	case "shift+tab", "up":
		m.focusIndex--
		if m.focusIndex < 0 {
			m.focusIndex = m.fieldCount() - 1
		}
	case "left", "h":
		m.cycle(-1)
	case "right", "l", " ":
		m.cycle(1)
	case "enter":
		if m.focusSave() {
			return m.save()
		}
		m.cycle(1)
	}
	return m, nil
}

func (m *AgentDefaultsModel) cycle(delta int) {
	if m.focusSave() {
		return
	}
	row, effort := m.focusCell()
	if row < 0 || row >= len(m.rows) {
		return
	}
	r := &m.rows[row]
	if effort {
		r.effortIdx = wrapIndex(r.effortIdx+delta, len(r.efforts))
		return
	}
	r.modelIdx = wrapIndex(r.modelIdx+delta, len(r.models))
}

func wrapIndex(i, n int) int {
	if n <= 0 {
		return 0
	}
	i %= n
	if i < 0 {
		i += n
	}
	return i
}

func (m AgentDefaultsModel) save() (tea.Model, tea.Cmd) {
	if m.cfg.AgentDefaults == nil {
		m.cfg.AgentDefaults = map[string]config.AgentDefaults{}
	}
	for _, r := range m.rows {
		d := config.AgentDefaults{Model: optionAt(r.models, r.modelIdx)}
		if r.hasEffort {
			d.Effort = optionAt(r.efforts, r.effortIdx)
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

func optionAt(values []string, i int) string {
	if i < 0 || i >= len(values) {
		return ""
	}
	return values[i]
}

func (m AgentDefaultsModel) viewString() string {
	if m.saved {
		return "Agent defaults saved!\n"
	}
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	var b strings.Builder
	b.WriteString("Agent defaults\n")
	b.WriteString("Pick a model and reasoning effort from each CLI's own list. Empty = agent default.\n")
	b.WriteString("Left/right cycles the highlighted value. Effort is n/a when the CLI has no such flag.\n\n")
	b.WriteString(fmt.Sprintf("  %-22s  %-28s  %s\n", "Agent", "Model", "Effort"))
	saveFocus := m.focusSave()
	focusRow, focusEffort := 0, false
	if !saveFocus {
		focusRow, focusEffort = m.focusCell()
	}
	for i, r := range m.rows {
		name := fmt.Sprintf("%-22s", r.name)
		if !saveFocus && i == focusRow {
			name = activeStyle.Render(name)
		}
		model := fmt.Sprintf("%-28s", formatOption(r.models, r.modelIdx))
		if !saveFocus && i == focusRow && !focusEffort {
			model = activeStyle.Render(model)
		}
		effort := dim.Render(fmt.Sprintf("%-16s", "n/a"))
		if r.hasEffort {
			effort = fmt.Sprintf("%-16s", formatOption(r.efforts, r.effortIdx))
			if !saveFocus && i == focusRow && focusEffort {
				effort = activeStyle.Render(effort)
			}
		}
		fmt.Fprintf(&b, "  %s  %s  %s\n", name, model, effort)
	}
	saveLabel := "[ Save ]"
	if saveFocus {
		saveLabel = activeStyle.Render(saveLabel)
	}
	b.WriteString("\n" + saveLabel + "\n")
	if m.err != nil {
		b.WriteString("\n" + failStyle.Render(fmt.Sprintf("Error: %v", m.err)) + "\n")
	}
	b.WriteString("\n(tab to move, ←/→ to change, enter to save, esc to cancel)\n")
	return docStyle.Render(b.String())
}

func formatOption(values []string, i int) string {
	v := optionAt(values, i)
	if v == "" {
		return "(agent default)"
	}
	return v
}

func (m AgentDefaultsModel) View() tea.View {
	return newView(m.viewString())
}
