package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/agent"
)

func needsModelPick(command string) bool {
	if agent.HasDynamicModels(command) {
		return true
	}
	return len(agent.LaunchCatalogFor(agent.CommandBase(command)).Models) > 1
}

func (m AgentPickerModel) startModelPick(a domain.Agent) (AgentPickerModel, tea.Cmd) {
	pending := a
	m.pending = &pending
	m.pick, m.filter, m.provider, m.input, m.loadErr = 0, "", "", "", nil
	m.dynamic = agent.HasDynamicModels(a.Command)
	if !m.dynamic {
		m.models = agent.StaticModels(a.Command)
		m.step = agentPickModel
		return m, nil
	}
	m.models = nil
	if m.lister == nil {
		m.loadErr = fmt.Errorf("no remote selected to list %s models", agent.CommandBase(a.Command))
		m.step, m.input = agentPickModelTyping, ""
		return m, nil
	}
	m.step = agentPickModelLoading
	list, command := m.lister, a.Command
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), preferredModelsTimeout)
		defer cancel()
		models, err := list(ctx, command)
		return preferredModelsMsg{command: command, models: models, err: err}
	}
}

func (m AgentPickerModel) applyModels(msg preferredModelsMsg) AgentPickerModel {
	if m.step != agentPickModelLoading || m.pending == nil || msg.command != m.pending.Command {
		return m
	}
	if msg.err != nil || len(msg.models) == 0 {
		m.loadErr = msg.err
		if m.loadErr == nil {
			m.loadErr = fmt.Errorf("no models listed")
		}
		m.step, m.input = agentPickModelTyping, ""
		return m
	}
	m.models, m.step, m.pick = msg.models, agentPickProvider, 0
	return m
}

func (m AgentPickerModel) confirm(a domain.Agent, model string) AgentPickerModel {
	a.LaunchModel = strings.TrimSpace(model)
	m.selected = &a
	m.step = agentPickList
	m.pending = nil
	return m
}

func (m AgentPickerModel) confirmPending(model string) AgentPickerModel {
	if m.pending == nil {
		return m
	}
	return m.confirm(*m.pending, model)
}

func (m AgentPickerModel) updateModelPick(km tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.step == agentPickModelLoading {
		if km.String() == "esc" {
			m.step, m.pending = agentPickList, nil
		}
		return m, nil
	}
	if m.step == agentPickModelTyping {
		return m.updateModelTyping(km)
	}
	return m.updateModelRows(km)
}

func (m AgentPickerModel) updateModelTyping(km tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch km.String() {
	case "esc":
		m.step, m.pending, m.input = agentPickList, nil, ""
	case "enter":
		return m.confirmPending(m.input), nil
	default:
		m.input = editText(m.input, km)
	}
	return m, nil
}

func (m AgentPickerModel) updateModelRows(km tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	rows := m.modelRows()
	key := km.String()
	filtering := m.step == agentPickModel
	switch {
	case key == "esc":
		return m.escapeModelPick(), nil
	case key == "down" || key == "ctrl+n" || (!filtering && key == "j"):
		if m.pick < len(rows)-1 {
			m.pick++
		}
	case key == "up" || key == "ctrl+p" || (!filtering && key == "k"):
		if m.pick > 0 {
			m.pick--
		}
	case key == "enter":
		if m.pick >= 0 && m.pick < len(rows) {
			return m.chooseModelRow(rows[m.pick])
		}
	case filtering && (key == "backspace" || km.Text != ""):
		m.filter = editText(m.filter, km)
		m.pick = 0
	}
	return m, nil
}

func (m AgentPickerModel) escapeModelPick() AgentPickerModel {
	if m.step == agentPickModel && m.dynamic {
		m.step, m.pick, m.filter = agentPickProvider, 0, ""
		return m
	}
	m.step, m.pending = agentPickList, nil
	return m
}

func (m AgentPickerModel) chooseModelRow(r pickRow) (tea.Model, tea.Cmd) {
	switch m.step {
	case agentPickProvider:
		return m.chooseProvider(r)
	case agentPickModel:
		if r.kind == rowOther {
			m.step, m.input = agentPickModelTyping, ""
			return m, nil
		}
		return m.confirmPending(r.value), nil
	}
	return m, nil
}

func (m AgentPickerModel) chooseProvider(r pickRow) (tea.Model, tea.Cmd) {
	switch r.kind {
	case rowProvider:
		m.step, m.provider, m.filter, m.pick = agentPickModel, r.value, "", 0
		return m, nil
	case rowOther:
		m.step, m.input = agentPickModelTyping, ""
		return m, nil
	}
	return m.confirmPending(""), nil
}

func (m AgentPickerModel) modelRows() []pickRow {
	switch m.step {
	case agentPickProvider:
		return providerRows(m.models)
	case agentPickModel:
		return catalogModelRows(m.models, m.provider, m.filter, m.dynamic)
	}
	return nil
}

func providerRows(models []agent.ModelOption) []pickRow {
	rows := []pickRow{{kind: rowDefault, label: "Default (agent's own default)"}}
	for _, p := range agent.ModelProviders(models) {
		rows = append(rows, pickRow{kind: rowProvider, value: p.Name, label: fmt.Sprintf("%s (%d)", p.Name, p.Count)})
	}
	return append(rows, pickRow{kind: rowOther, label: "Other (type a model)"})
}

func catalogModelRows(models []agent.ModelOption, provider, filter string, dynamic bool) []pickRow {
	var rows []pickRow
	if !dynamic {
		rows = append(rows, pickRow{kind: rowDefault, label: "Default (agent's own default)"})
	}
	f := strings.ToLower(filter)
	for _, o := range models {
		if dynamic && o.Provider != provider {
			continue
		}
		if f != "" && !strings.Contains(strings.ToLower(o.ID), f) {
			continue
		}
		label := o.Label
		if label == "" {
			label = o.ID
		}
		rows = append(rows, pickRow{kind: rowValue, value: o.ID, label: label})
	}
	if !dynamic {
		rows = append(rows, pickRow{kind: rowOther, label: "Other (type a model)"})
	}
	return rows
}

func (m AgentPickerModel) viewModelPick() string {
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	var b strings.Builder
	title := "Select model"
	if m.pending != nil {
		title += " for " + m.pending.Name
	}
	b.WriteString(title + "\n")
	if m.dynamic && m.host != "" {
		b.WriteString(dim.Render("models from "+m.host) + "\n")
	}
	switch m.step {
	case agentPickModelLoading:
		b.WriteString("  Listing models on " + m.host + "...\n")
	case agentPickModelTyping:
		if m.loadErr != nil {
			b.WriteString(failStyle.Render(fmt.Sprintf("  Could not list models: %v", m.loadErr)) + "\n")
		}
		b.WriteString("  Model (empty for the agent's default): " + m.input + "\n")
	case agentPickProvider:
		b.WriteString("  Provider:\n")
		m.writeModelRows(&b, dim)
	case agentPickModel:
		label := "  Model"
		if m.provider != "" {
			label += " (" + m.provider + ")"
		}
		b.WriteString(label + ", filter: " + m.filter + "\n")
		m.writeModelRows(&b, dim)
	}
	b.WriteString("\n" + dim.Render("(arrows choose, enter select, esc back)") + "\n")
	return docStyle.Render(b.String())
}

func (m AgentPickerModel) writeModelRows(b *strings.Builder, dim lipgloss.Style) {
	rows := m.modelRows()
	if len(rows) == 0 {
		b.WriteString(dim.Render("    (no match)") + "\n")
		return
	}
	start := 0
	if m.pick >= pickWindow {
		start = m.pick - pickWindow + 1
	}
	end := min(len(rows), start+pickWindow)
	for i := start; i < end; i++ {
		writePickRow(b, rows[i].label, i == m.pick)
	}
	if more := len(rows) - end; more > 0 {
		b.WriteString(dim.Render(fmt.Sprintf("    (%d more)", more)) + "\n")
	}
}
