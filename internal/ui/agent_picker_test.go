package ui

import (
	"testing"

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
	m := NewAgentPickerModel([]domain.Agent{{Name: "Grok", Command: "grok"}})
	next, _ := m.Update(pressKey("enter"))
	got := next.(AgentPickerModel).selected
	if got == nil || got.Command != "grok" {
		t.Fatalf("selected = %+v, want grok", got)
	}
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
