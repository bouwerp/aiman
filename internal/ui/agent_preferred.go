package ui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/ssh"
)

// preferredChoice is one supported agent the add picker offers.
type preferredChoice struct {
	name    string
	command string
}

// modelLister lists the models an agent command can launch with on the
// editor's remote.
type modelLister func(ctx context.Context, command string) ([]agent.ModelOption, error)

// preferredModelsMsg carries a model listing back to the editor.
type preferredModelsMsg struct {
	command string
	models  []agent.ModelOption
	err     error
}

// preferredModelsTimeout bounds one model listing on the remote.
const preferredModelsTimeout = 45 * time.Second

// preferredModelCache keeps Pi model listings per host for the life of the TUI.
var preferredModelCache = agent.NewModelCache()

// remoteModelLister lists models on remote over SSH through the shared cache.
func remoteModelLister(remote config.Remote) modelLister {
	if remote.Host == "" {
		return nil
	}
	return func(ctx context.Context, command string) ([]agent.ModelOption, error) {
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		return preferredModelCache.List(ctx, remote.Host, mgr, command)
	}
}

// preferredStep is where the editor is in adding or editing an entry.
type preferredStep int

const (
	stepList preferredStep = iota
	stepAgent
	stepAgentTyping
	stepModelLoading
	stepProvider
	stepModel
	stepModelTyping
	stepEffort
)

// pickRowKind tells what choosing a row in a model or effort step does.
type pickRowKind int

const (
	rowDefault pickRowKind = iota
	rowValue
	rowProvider
	rowOther
)

type pickRow struct {
	kind  pickRowKind
	value string
	label string
}

// pickWindow is how many rows a model or effort list shows at once.
const pickWindow = 12

// AgentPreferredModel edits the two model tiers: agents.smart and
// agents.preferred (the worker tier), each an ordered list of agents with an
// optional model and effort. The active tab is edited in place.
type AgentPreferredModel struct {
	cfg *config.Config
	// tab is the tier being edited; drafts and cursors hold both tiers so a
	// tab switch keeps unsaved work.
	tab     string
	items   []config.PreferredAgent
	cursor  int
	drafts  map[string][]config.PreferredAgent
	cursors map[string]int

	// host names the remote whose models the editor lists; list runs there.
	host string
	list modelLister

	step preferredStep
	// editIndex is the entry e is editing, or -1 while adding one.
	editIndex int
	draft     config.PreferredAgent
	// choices are the supported agents; len(choices) is the row that
	// switches to typing a command.
	choices []preferredChoice
	pick    int
	input   string
	// models is the agent's full model list; provider narrows a dynamic
	// list to one provider and filter narrows the model step further.
	models   []agent.ModelOption
	dynamic  bool
	provider string
	filter   string
	loadErr  error

	saved bool
	err   error
}

// NewAgentPreferredModel opens the editor on cfg's model tiers, on the tab
// that has entries. host and list are the remote models are listed from; a nil
// list means model choice falls back to typing for agents that list models on
// the host.
func NewAgentPreferredModel(cfg *config.Config, host string, list modelLister) AgentPreferredModel {
	m := AgentPreferredModel{
		cfg: cfg, host: host, list: list, editIndex: -1,
		drafts:  map[string][]config.PreferredAgent{},
		cursors: map[string]int{},
	}
	for _, tier := range tierTabs {
		var entries []config.PreferredAgent
		if cfg != nil {
			entries = cloneEntries(cfg.Agents.Tier(tier))
		}
		m.drafts[tier] = entries
	}
	// Open on the tier that has entries: a fresh worker-only config lands on
	// the tab its list is on, and a smart tier is the first thing to see.
	m.tab = config.TierWorker
	if cfg != nil && len(cfg.Agents.Smart) > 0 {
		m.tab = config.TierSmart
	}
	m.items = m.drafts[m.tab]
	return m
}

// tierTabs is the tab order on the page.
var tierTabs = []string{config.TierSmart, config.TierWorker}

// cloneEntries copies entries, keeping a nil list nil so an unset tier stays
// unset instead of being written as an empty list.
func cloneEntries(entries []config.PreferredAgent) []config.PreferredAgent {
	if entries == nil {
		return nil
	}
	return append([]config.PreferredAgent{}, entries...)
}

// stash records the active tab's list and cursor before a tab switch or save.
func (m *AgentPreferredModel) stash() {
	m.drafts[m.tab] = m.items
	m.cursors[m.tab] = m.cursor
}

// switchTab makes tier the active tab, keeping the other tab's unsaved work.
func (m *AgentPreferredModel) switchTab(tier string) {
	if tier == m.tab || !config.ValidTier(tier) {
		return
	}
	m.stash()
	m.tab = tier
	m.items = m.drafts[tier]
	m.cursor = m.cursors[tier]
	if m.cursor >= len(m.items) {
		m.cursor = max(0, len(m.items)-1)
	}
}

// nextTab is the tier tab in direction (+1 forward, -1 back), wrapping.
func (m AgentPreferredModel) nextTab(dir int) string {
	i := slices.Index(tierTabs, m.tab)
	if i < 0 {
		return config.TierSmart
	}
	return tierTabs[(i+dir+len(tierTabs))%len(tierTabs)]
}

func (m AgentPreferredModel) Init() tea.Cmd { return nil }

// inFlow reports whether an add or edit is under way, so esc belongs to it.
func (m AgentPreferredModel) inFlow() bool { return m.step != stepList }

func (m AgentPreferredModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if lm, ok := msg.(preferredModelsMsg); ok {
		return m.applyModels(lm), nil
	}
	km, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	if km.String() == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.step {
	case stepAgent:
		return m.updateAgent(km)
	case stepAgentTyping:
		return m.updateAgentTyping(km)
	case stepModelLoading:
		if km.String() == "esc" {
			m.step = stepList
		}
		return m, nil
	case stepProvider, stepModel, stepEffort:
		return m.updatePick(km)
	case stepModelTyping:
		return m.updateModelTyping(km)
	}
	return m.updateList(km)
}

func (m AgentPreferredModel) updateList(km tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch km.String() {
	case "tab":
		m.switchTab(m.nextTab(1))
		return m, nil
	case "shift+tab":
		m.switchTab(m.nextTab(-1))
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
		m.step, m.editIndex, m.input, m.pick = stepAgent, -1, "", 0
		m.choices = supportedChoices()
	case "e":
		if m.cursor >= 0 && m.cursor < len(m.items) {
			m.editIndex = m.cursor
			return m.startModelStep(m.items[m.cursor])
		}
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

// supportedChoices lists each supported agent command once. An agent already
// in the list stays on offer, since it may be preferred again with another
// model.
func supportedChoices() []preferredChoice {
	var out []preferredChoice
	seen := map[string]bool{}
	for _, a := range agent.KnownAgents() {
		cmd := agent.CommandBase(a.Command)
		if cmd == "" || seen[cmd] {
			continue
		}
		seen[cmd] = true
		out = append(out, preferredChoice{name: a.Name, command: cmd})
	}
	return out
}

func (m AgentPreferredModel) updateAgent(km tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch km.String() {
	case "esc":
		m.step = stepList
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
			m.step, m.input = stepAgentTyping, ""
			return m, nil
		}
		return m.startModelStep(config.PreferredAgent{Agent: m.choices[m.pick].command})
	}
	return m, nil
}

func (m AgentPreferredModel) updateAgentTyping(km tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch km.String() {
	case "esc":
		m.step, m.input = stepAgent, ""
	case "enter":
		cmd := strings.TrimSpace(m.input)
		m.input = ""
		if cmd == "" {
			m.step = stepList
			return m, nil
		}
		return m.startModelStep(config.PreferredAgent{Agent: cmd})
	default:
		m.input = editText(m.input, km)
	}
	return m, nil
}

// startModelStep begins choosing a model for draft's agent: a static catalog
// at once, or a listing from the remote for agents that list their own.
func (m AgentPreferredModel) startModelStep(draft config.PreferredAgent) (tea.Model, tea.Cmd) {
	m.draft = draft
	m.pick, m.filter, m.provider, m.input, m.loadErr = 0, "", "", "", nil
	m.dynamic = agent.HasDynamicModels(draft.Agent)
	if !m.dynamic {
		m.models = agent.StaticModels(draft.Agent)
		m.step = stepModel
		return m, nil
	}
	m.models = nil
	if m.list == nil {
		m.loadErr = fmt.Errorf("no remote selected to list %s models", draft.Command())
		m.step, m.input = stepModelTyping, draft.Model
		return m, nil
	}
	m.step = stepModelLoading
	list, command := m.list, draft.Agent
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), preferredModelsTimeout)
		defer cancel()
		models, err := list(ctx, command)
		return preferredModelsMsg{command: command, models: models, err: err}
	}
}

func (m AgentPreferredModel) applyModels(msg preferredModelsMsg) AgentPreferredModel {
	if m.step != stepModelLoading || msg.command != m.draft.Agent {
		return m
	}
	if msg.err != nil || len(msg.models) == 0 {
		m.loadErr = msg.err
		if m.loadErr == nil {
			m.loadErr = fmt.Errorf("no models listed")
		}
		m.step, m.input = stepModelTyping, m.draft.Model
		return m
	}
	m.models, m.step, m.pick = msg.models, stepProvider, 0
	return m
}

// rows lists what the current pick step offers, after the filter.
func (m AgentPreferredModel) rows() []pickRow {
	switch m.step {
	case stepProvider:
		rows := []pickRow{{kind: rowDefault, label: "Default (agent's own default)"}}
		for _, p := range agent.ModelProviders(m.models) {
			rows = append(rows, pickRow{kind: rowProvider, value: p.Name, label: fmt.Sprintf("%s (%d)", p.Name, p.Count)})
		}
		return append(rows, pickRow{kind: rowOther, label: "Other (type a model)"})
	case stepModel:
		var rows []pickRow
		if !m.dynamic {
			rows = append(rows, pickRow{kind: rowDefault, label: "Default (agent's own default)"})
		}
		f := strings.ToLower(m.filter)
		for _, o := range m.models {
			if m.dynamic && o.Provider != m.provider {
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
		if !m.dynamic {
			rows = append(rows, pickRow{kind: rowOther, label: "Other (type a model)"})
		}
		return rows
	case stepEffort:
		rows := []pickRow{{kind: rowDefault, label: "Default (agent's own default)"}}
		for _, e := range agent.LaunchCatalogFor(agent.CommandBase(m.draft.Agent)).Efforts {
			rows = append(rows, pickRow{kind: rowValue, value: e, label: e})
		}
		return rows
	}
	return nil
}

// updatePick handles the provider, model and effort lists. Arrows move; in
// the model list printable keys filter, elsewhere j and k also move.
func (m AgentPreferredModel) updatePick(km tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	rows := m.rows()
	key := km.String()
	filtering := m.step == stepModel
	switch {
	case key == "esc":
		if m.step == stepModel && m.dynamic {
			m.step, m.pick, m.filter = stepProvider, 0, ""
			return m, nil
		}
		m.step = stepList
	case key == "down" || key == "ctrl+n" || (!filtering && key == "j"):
		if m.pick < len(rows)-1 {
			m.pick++
		}
	case key == "up" || key == "ctrl+p" || (!filtering && key == "k"):
		if m.pick > 0 {
			m.pick--
		}
	case key == "enter":
		if m.pick < 0 || m.pick >= len(rows) {
			return m, nil
		}
		return m.choose(rows[m.pick])
	case filtering && (key == "backspace" || km.Text != ""):
		m.filter = editText(m.filter, km)
		m.pick = 0
	}
	return m, nil
}

func (m AgentPreferredModel) choose(r pickRow) (tea.Model, tea.Cmd) {
	switch m.step {
	case stepProvider:
		switch r.kind {
		case rowProvider:
			m.step, m.provider, m.filter, m.pick = stepModel, r.value, "", 0
			return m, nil
		case rowOther:
			m.step, m.input = stepModelTyping, m.draft.Model
			return m, nil
		}
		m.draft.Model = ""
		return m.startEffortStep()
	case stepModel:
		if r.kind == rowOther {
			m.step, m.input = stepModelTyping, m.draft.Model
			return m, nil
		}
		m.draft.Model = r.value
		return m.startEffortStep()
	case stepEffort:
		m.draft.Effort = r.value
		return m.commit(), nil
	}
	return m, nil
}

func (m AgentPreferredModel) updateModelTyping(km tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch km.String() {
	case "esc":
		m.step, m.input = stepList, ""
	case "enter":
		m.draft.Model = strings.TrimSpace(m.input)
		m.input = ""
		return m.startEffortStep()
	default:
		m.input = editText(m.input, km)
	}
	return m, nil
}

// startEffortStep offers the agent's effort levels, or commits the entry when
// the agent has none.
func (m AgentPreferredModel) startEffortStep() (tea.Model, tea.Cmd) {
	efforts := agent.LaunchCatalogFor(agent.CommandBase(m.draft.Agent)).Efforts
	if len(efforts) == 0 {
		m.draft.Effort = ""
		return m.commit(), nil
	}
	m.step, m.pick, m.filter = stepEffort, 0, ""
	if i := slices.Index(efforts, m.draft.Effort); i >= 0 {
		m.pick = i + 1
	}
	return m, nil
}

// commit stores the draft: in place when editing, otherwise appended unless
// an identical entry is already listed.
func (m AgentPreferredModel) commit() AgentPreferredModel {
	d := config.PreferredAgent{
		Agent:  m.draft.Command(),
		Model:  strings.TrimSpace(m.draft.Model),
		Effort: strings.TrimSpace(m.draft.Effort),
	}
	m.step = stepList
	if m.editIndex >= 0 && m.editIndex < len(m.items) {
		m.items[m.editIndex] = d
		m.cursor = m.editIndex
		m.editIndex = -1
		return m
	}
	if i := slices.Index(m.items, d); i >= 0 {
		m.cursor = i
		return m
	}
	m.items = append(m.items, d)
	m.cursor = len(m.items) - 1
	return m
}

// editText applies backspace or a typed key to s.
func editText(s string, km tea.KeyPressMsg) string {
	if km.String() == "backspace" {
		if s == "" {
			return s
		}
		r := []rune(s)
		return string(r[:len(r)-1])
	}
	return s + km.Text
}

func (m AgentPreferredModel) save() (tea.Model, tea.Cmd) {
	if m.cfg == nil {
		m.err = fmt.Errorf("config is not loaded")
		return m, nil
	}
	m.stash()
	for _, tier := range tierTabs {
		m.cfg.Agents.SetTier(tier, cloneEntries(m.drafts[tier]))
	}
	if err := m.cfg.Save(); err != nil {
		m.err = err
		return m, nil
	}
	m.saved = true
	return m, nil
}

func (m AgentPreferredModel) viewString() string {
	if m.saved {
		return "Model tiers saved!\n"
	}
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	var b strings.Builder
	b.WriteString("Model tiers\n")
	b.WriteString("Smart and worker agents and models, in order. The server launches the first installed entry of the tier it is asked for.\n\n")
	b.WriteString(m.tabBar(dim) + "\n")
	b.WriteString(dim.Render("  Used for: "+tierUsedFor(m.tab)) + "\n")
	if len(m.items) == 0 {
		b.WriteString(dim.Render("  (empty: "+tierEmptyHint(m.tab)+")") + "\n")
	}
	for i, p := range m.items {
		row := fmt.Sprintf("%2d. %s", i+1, p.Label())
		if i == m.cursor && !m.inFlow() {
			b.WriteString(activeStyle.Render(row) + "\n")
		} else {
			b.WriteString("  " + row + "\n")
		}
	}
	if m.inFlow() {
		b.WriteString("\n" + m.flowView(dim))
	}
	if m.err != nil {
		b.WriteString("\n" + failStyle.Render(fmt.Sprintf("Error: %v", m.err)) + "\n")
	}
	b.WriteString("\n" + m.help() + "\n")
	return docStyle.Render(b.String())
}

func (m AgentPreferredModel) help() string {
	switch m.step {
	case stepList:
		return "(tab switches tier, j/k or arrows move, J/K reorder, a add, e edit model/effort, d delete, enter save, esc cancel)"
	case stepAgent, stepProvider, stepEffort:
		return "(j/k or arrows choose, enter select, esc back)"
	case stepModel:
		return "(type to filter, arrows choose, enter select, esc back)"
	case stepModelLoading:
		return "(esc cancel)"
	}
	return "(enter accept, esc back)"
}

func (m AgentPreferredModel) flowView(dim lipgloss.Style) string {
	var b strings.Builder
	switch m.step {
	case stepAgent:
		b.WriteString("  Add a supported agent:\n")
		for i, c := range m.choices {
			writePickRow(&b, fmt.Sprintf("%-16s %s", c.command, dim.Render(c.name)), i == m.pick)
		}
		writePickRow(&b, "Other (type a command)", m.pick == len(m.choices))
		return b.String()
	case stepAgentTyping:
		return "  Agent command: " + m.input + "\n"
	}
	title := m.draft.Command()
	if m.draft.Model != "" && m.step == stepEffort {
		title += "  " + m.draft.Model
	}
	b.WriteString("  " + title + "\n")
	if m.dynamic && m.host != "" {
		b.WriteString(dim.Render("  models from "+m.host) + "\n")
	}
	switch m.step {
	case stepModelLoading:
		b.WriteString("  Listing models on " + m.host + "...\n")
	case stepModelTyping:
		if m.loadErr != nil {
			b.WriteString(failStyle.Render(fmt.Sprintf("  Could not list models: %v", m.loadErr)) + "\n")
		}
		b.WriteString("  Model (empty for the agent's default): " + m.input + "\n")
	case stepProvider:
		b.WriteString("  Provider:\n")
		m.writeRows(&b, dim)
	case stepModel:
		label := "Model"
		if m.provider != "" {
			label += " (" + m.provider + ")"
		}
		b.WriteString("  " + label + ", filter: " + m.filter + "\n")
		m.writeRows(&b, dim)
	case stepEffort:
		b.WriteString("  Effort:\n")
		m.writeRows(&b, dim)
	}
	return b.String()
}

// tabBar is the smart/worker tab strip with each tier's entry count.
func (m AgentPreferredModel) tabBar(dim lipgloss.Style) string {
	parts := make([]string, 0, len(tierTabs))
	for _, tier := range tierTabs {
		count := len(m.drafts[tier])
		if tier == m.tab {
			count = len(m.items)
		}
		label := fmt.Sprintf(" %s (%d) ", tier, count)
		if tier == m.tab {
			parts = append(parts, activeStyle.Render(label))
			continue
		}
		parts = append(parts, dim.Render(label))
	}
	return "  " + strings.Join(parts, " ")
}

// tierUsedFor is the one-line guidance under the tab strip.
func tierUsedFor(tier string) string {
	if tier == config.TierSmart {
		return "planning or design, ambiguous debugging, cross-cutting integration, review, long-running work"
	}
	return "a well-specified task: mechanical edits, a small diff, making tests pass"
}

// tierEmptyHint says what an empty tier falls back to.
func tierEmptyHint(tier string) string {
	if tier == config.TierSmart {
		return "a smart request falls back to the worker tier"
	}
	return "a worker request falls back to the smart tier"
}

// writeRows renders a window of pickWindow rows around the selection.
func (m AgentPreferredModel) writeRows(b *strings.Builder, dim lipgloss.Style) {
	rows := m.rows()
	if len(rows) == 0 {
		b.WriteString(dim.Render("    (no match)") + "\n")
		return
	}
	start := 0
	if m.pick >= pickWindow {
		start = m.pick - pickWindow + 1
	}
	end := min(start+pickWindow, len(rows))
	for i := start; i < end; i++ {
		writePickRow(b, rows[i].label, i == m.pick)
	}
	if more := len(rows) - end; more > 0 {
		b.WriteString(dim.Render(fmt.Sprintf("    (%d more)", more)) + "\n")
	}
}

func writePickRow(b *strings.Builder, row string, selected bool) {
	if selected {
		b.WriteString("  " + activeStyle.Render("> "+row) + "\n")
		return
	}
	b.WriteString("    " + row + "\n")
}

func (m AgentPreferredModel) View() tea.View {
	return newView(m.viewString())
}
