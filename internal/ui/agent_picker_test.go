package ui

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
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
