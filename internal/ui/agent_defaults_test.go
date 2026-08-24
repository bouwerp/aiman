package ui

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/config"
	tea "github.com/charmbracelet/bubbletea"
)

func TestAgentDefaultsModelListsKnownAgents(t *testing.T) {
	m := NewAgentDefaultsModel(&config.Config{})
	if len(m.rows) == 0 {
		t.Fatal("expected known agents")
	}
	out := m.View()
	if !strings.Contains(out, "Claude") || !strings.Contains(out, "Grok") {
		t.Fatalf("%s", out)
	}
	if !strings.Contains(out, "n/a") {
		t.Fatal("expected greyed n/a effort")
	}
}

func TestAgentDefaultsEffortAvailability(t *testing.T) {
	m := NewAgentDefaultsModel(&config.Config{})
	got := map[string]bool{}
	for _, r := range m.rows {
		got[r.key] = r.hasEffort
	}
	if !got["claude"] || !got["grok"] || !got["codex"] || !got["agy"] || !got["copilot"] || !got["pi"] {
		t.Fatalf("effort agents: %+v", got)
	}
	if got["opencode"] || got["cursor-agent"] {
		t.Fatalf("n/a agents must not expose effort: %+v", got)
	}
}

func TestAgentDefaultsCyclesModelList(t *testing.T) {
	m := NewAgentDefaultsModel(&config.Config{AgentDefaults: map[string]config.AgentDefaults{
		"claude": {Model: "sonnet"},
	}})
	row := rowByKey(t, m, "claude")
	if m.rows[row].models[m.rows[row].modelIdx] != "sonnet" {
		t.Fatalf("%q", m.rows[row].models[m.rows[row].modelIdx])
	}
	m.focusIndex = fieldIndex(m, "claude", false)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = next.(AgentDefaultsModel)
	if m.rows[row].models[m.rows[row].modelIdx] == "sonnet" {
		t.Fatal("right should cycle off sonnet")
	}
}

func TestAgentDefaultsTabSkipsNAEffort(t *testing.T) {
	m := NewAgentDefaultsModel(&config.Config{})
	m.focusIndex = fieldIndex(m, "opencode", false)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(AgentDefaultsModel)
	row, effort := m.focusCell()
	if m.rows[row].key == "opencode" && effort {
		t.Fatal("tab must skip n/a effort")
	}
}

func rowByKey(t *testing.T, m AgentDefaultsModel, key string) int {
	t.Helper()
	for i, r := range m.rows {
		if r.key == key {
			return i
		}
	}
	t.Fatalf("missing %s", key)
	return -1
}

func fieldIndex(m AgentDefaultsModel, key string, effort bool) int {
	n := 0
	for _, r := range m.rows {
		if r.key == key && !effort {
			return n
		}
		n++
		if r.hasEffort {
			if r.key == key && effort {
				return n
			}
			n++
		}
	}
	return 0
}
