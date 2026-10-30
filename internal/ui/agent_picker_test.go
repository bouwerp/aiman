package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// With nothing to select, enter leaves the choice to serve's agents.preferred
// rather than substituting an agent here.
func TestAgentPickerEnterWithoutAgentsUsesPreferred(t *testing.T) {
	m := NewAgentPickerModel(nil)
	m.allowPreferred = true
	next, _ := m.Update(pressKey("enter"))
	got := next.(AgentPickerModel).selected
	if got == nil {
		t.Fatal("enter with no agents selected nothing")
	}
	if !isPreferredAgentChoice(got) || got.Command != "" {
		t.Fatalf("selected = %+v, want the preferred choice with no command", got)
	}
}

// A restart picker never offers the agents.preferred choice.
func TestAgentPickerWithoutPreferredSelectsNothing(t *testing.T) {
	next, _ := NewAgentPickerModel(nil).Update(pressKey("enter"))
	if got := next.(AgentPickerModel).selected; got != nil {
		t.Fatalf("selected = %+v, want nothing", got)
	}
}

func TestAgentPickerEnterSelectsListedAgent(t *testing.T) {
	m := sendPicker(t, NewAgentPickerModel([]domain.Agent{{Name: "Grok", Command: "grok"}}), pressKey("enter"))
	if m.selected != nil {
		t.Fatalf("enter on grok selected %+v before a model was chosen", m.selected)
	}
	if !m.inModelFlow() {
		t.Fatal("enter on grok must open the model step")
	}
	m = sendPicker(t, m, pressKey("enter"))
	if m.selected == nil || m.selected.Command != "grok" || m.selected.LaunchModel != "" {
		t.Fatalf("default model pick = %+v, want grok with empty launch model", m.selected)
	}
}

func sendPicker(t *testing.T, m AgentPickerModel, msg tea.Msg) AgentPickerModel {
	t.Helper()
	next, cmd := m.Update(msg)
	m = next.(AgentPickerModel)
	if cmd == nil {
		return m
	}
	if out := cmd(); out != nil {
		if _, quit := out.(tea.QuitMsg); !quit {
			return sendPicker(t, m, out)
		}
	}
	return m
}

func TestPreferredChoiceIsHandedToServe(t *testing.T) {
	cfg := domain.SessionConfig{Agent: preferredAgentChoice(), Repo: domain.Repo{Name: "r"}, Branch: "b"}
	if ok, why := canCreateRemotely(cfg); !ok {
		t.Fatalf("preferred choice refused remotely: %s", why)
	}
	spec := remoteCreateSpecFrom(cfg)
	if spec.Agent != "" || spec.AgentName != "" {
		t.Fatalf("spec agent = %q / %q, want both empty so serve picks", spec.Agent, spec.AgentName)
	}
}

func TestAgentPickerPreselect(t *testing.T) {
	agents := []domain.Agent{
		{Name: "Claude Code", Command: "claude --dangerously-skip-permissions"},
		{Name: "Codex", Command: "codex"},
	}
	m := NewAgentPickerModel(agents)
	if !m.Preselect("codex") {
		t.Fatal("Preselect(codex) = false")
	}
	if got, ok := m.list.SelectedItem().(agentItem); !ok || got.agent.Command != "codex" {
		t.Fatalf("selected %+v, want codex", m.list.SelectedItem())
	}

	m = NewAgentPickerModel(agents)
	if m.Preselect("grok") {
		t.Fatal("Preselect(grok) = true for an agent that is not installed")
	}
	if m.Preselect("") {
		t.Fatal(`Preselect("") = true`)
	}
	if m.list.Index() != 0 {
		t.Fatalf("cursor %d, want 0", m.list.Index())
	}
}

// The create wizard starts the picker on the reviewer role's agent; a restart
// picker does not.
func TestWizardScanPreselectsRoleAgent(t *testing.T) {
	agents := []domain.Agent{{Name: "Claude Code", Command: "claude"}, {Name: "Codex", Command: "codex"}}
	cfg := &config.Config{Roles: config.RolesConfig{Verifier: config.RoleOverride{Agent: "codex"}}}

	m := NewModel(cfg, nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.sessionCfg.Role = domain.SessionRoleReviewer
	m.loadingNext = viewStateAgentPicker
	m.applyScanAgents(agent.ScanAgentsMsg{Agents: agents})
	if got, ok := m.agentPicker.list.SelectedItem().(agentItem); !ok || got.agent.Command != "codex" {
		t.Fatalf("wizard picker on %+v, want codex", m.agentPicker.list.SelectedItem())
	}

	m.loadingNext = viewStateRestartAgentPicker
	m.applyScanAgents(agent.ScanAgentsMsg{Agents: agents})
	if m.agentPicker.list.Index() != 0 {
		t.Fatalf("restart picker cursor %d, want 0", m.agentPicker.list.Index())
	}
}

// The create picker lists installed agents only, with no tier rows.
func TestAgentPickerListsOnlyAgents(t *testing.T) {
	agents := []domain.Agent{
		{Name: "Claude Code", Command: "claude"},
		{Name: "Codex", Command: "codex"},
	}
	m := NewAgentPickerModel(agents)
	m.allowPreferred = true
	items := m.list.Items()
	if len(items) != len(agents) {
		t.Fatalf("items = %d, want %d agent rows", len(items), len(agents))
	}
	for i, it := range items {
		if _, ok := it.(agentItem); !ok {
			t.Fatalf("item %d = %T, want agentItem", i, it)
		}
	}
	m = sendPicker(t, m, pressKey("down"))
	m = sendPicker(t, m, pressKey("enter"))
	if m.selected != nil {
		t.Fatalf("enter on codex selected %+v before a model was chosen", m.selected)
	}
	m = sendPicker(t, m, pressKey("enter"))
	if m.selected == nil || m.selected.Command != "codex" {
		t.Fatalf("selected = %+v, want codex", m.selected)
	}
}

func TestTierChoiceIsSentToServe(t *testing.T) {
	cfg := domain.SessionConfig{
		Agent: preferredAgentChoice(), Tier: config.TierSmart,
		Repo: domain.Repo{Name: "r"}, Branch: "b",
	}
	if ok, why := canCreateRemotely(cfg); !ok {
		t.Fatalf("tier choice refused remotely: %s", why)
	}
	spec := remoteCreateSpecFrom(cfg)
	if spec.Agent != "" || spec.Tier != config.TierSmart {
		t.Fatalf("spec = %+v, want tier smart and no agent", spec)
	}
}

func TestAgentPickerShowsDefaultModelInList(t *testing.T) {
	claude := domain.Agent{Name: "Claude Code", Command: "claude", Description: "Anthropic's Claude Code CLI"}
	pi := domain.Agent{Name: "Pi", Command: "pi", Description: "Pi coding agent (pi.dev)"}
	m := NewAgentPickerModel([]domain.Agent{claude, pi})
	m.SetModelSource("", nil, map[string]string{"claude": "opus"})
	items := m.list.Items()
	got := items[0].(agentItem).Description()
	if !strings.Contains(got, "opus") {
		t.Fatalf("claude description %q, want the configured default model", got)
	}
	got = items[1].(agentItem).Description()
	if !strings.Contains(got, "host default") {
		t.Fatalf("pi description %q, want host default when none is configured", got)
	}
}

func TestAgentPickerPicksCatalogModel(t *testing.T) {
	m := sendPicker(t, NewAgentPickerModel([]domain.Agent{{Name: "Grok", Command: "grok"}}), pressKey("enter"))
	m = pickPickerRow(t, m, "grok-4.5")
	if m.selected == nil || m.selected.Command != "grok" || m.selected.LaunchModel != "grok-4.5" {
		t.Fatalf("selected = %+v, want grok / grok-4.5", m.selected)
	}
}

func TestAgentPickerPiPicksOpenRouterModel(t *testing.T) {
	m := NewAgentPickerModel([]domain.Agent{{Name: "Pi", Command: "pi"}})
	m.SetModelSource("dev-box", fakeLister(piModels, nil), nil)
	m = sendPicker(t, m, pressKey("enter"))
	if !m.inModelFlow() {
		t.Fatal("pi must open the model step so OpenRouter and Ollama models can be chosen")
	}
	m = pickPickerRow(t, m, "openrouter")
	m = pickPickerRow(t, m, "qwen/qwen3-coder  ")
	if m.selected == nil || m.selected.Command != "pi" || m.selected.LaunchModel != "openrouter/qwen/qwen3-coder" {
		t.Fatalf("selected = %+v, want pi / openrouter/qwen/qwen3-coder", m.selected)
	}
}

func TestAgentPickerSkipsModelWhenAgentHasNone(t *testing.T) {
	m := sendPicker(t, NewAgentPickerModel([]domain.Agent{{Name: "Ageni", Command: "ageni"}}), pressKey("enter"))
	if m.inModelFlow() {
		t.Fatal("an agent with no catalog must not open the model step")
	}
	if m.selected == nil || m.selected.Command != "ageni" {
		t.Fatalf("selected = %+v, want ageni", m.selected)
	}
}

func TestAgentPickerEscFromModelReturnsToAgentList(t *testing.T) {
	m := sendPicker(t, NewAgentPickerModel([]domain.Agent{{Name: "Grok", Command: "grok"}}), pressKey("enter"))
	m = sendPicker(t, m, pressKey("esc"))
	if m.inModelFlow() {
		t.Fatal("esc must leave the model step")
	}
	if m.selected != nil {
		t.Fatalf("esc selected %+v", m.selected)
	}
}

func TestHandleAgentPickerEscDuringModelStaysOnPicker(t *testing.T) {
	cfg := twoRemoteCfg()
	m := NewModel(cfg, nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.state = viewStateAgentPicker
	m.agentPicker = NewAgentPickerModel([]domain.Agent{{Name: "Grok", Command: "grok"}})
	got, _ := m.handleAgentPickerUpdate(pressKey("enter"))
	m = got.(*Model)
	if m.state != viewStateAgentPicker || m.sessionCfg.Agent != nil {
		t.Fatalf("state=%v agent=%v after choosing grok, want still on the picker", m.state, m.sessionCfg.Agent)
	}
	got, _ = m.handleAgentPickerUpdate(pressKey("esc"))
	m = got.(*Model)
	if m.state != viewStateAgentPicker {
		t.Fatalf("esc from the model step left the picker: state=%v", m.state)
	}
	got, _ = m.handleAgentPickerUpdate(pressKey("esc"))
	m = got.(*Model)
	if m.state != viewStateDirPicker {
		t.Fatalf("esc from the agent list = %v, want dir picker", m.state)
	}
}

func pickPickerRow(t *testing.T, m AgentPickerModel, prefix string) AgentPickerModel {
	t.Helper()
	rows := m.modelRows()
	i := -1
	for j, r := range rows {
		if strings.HasPrefix(r.label, prefix) {
			i = j
			break
		}
	}
	if i < 0 {
		t.Fatalf("no row %q in %+v", prefix, rows)
	}
	for m.pick < i {
		m = sendPicker(t, m, pressKey("down"))
	}
	for m.pick > i {
		m = sendPicker(t, m, pressKey("up"))
	}
	return sendPicker(t, m, pressKey("enter"))
}
