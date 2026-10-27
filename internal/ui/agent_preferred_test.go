package ui

import (
	"testing"

	"github.com/bouwerp/aiman/internal/infra/config"
)

func TestPreferredEditorReordersAndSaves(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{Agents: config.AgentsConfig{Preferred: []string{"claude", "grok"}}}
	m := NewAgentPreferredModel(cfg)

	next, _ := m.Update(pressKey("down"))
	m = next.(AgentPreferredModel)
	next, _ = m.Update(pressKey("J"))
	m = next.(AgentPreferredModel)
	if len(m.items) != 2 || m.items[0] != "grok" || m.items[1] != "claude" {
		t.Fatalf("reorder = %v", m.items)
	}

	next, _ = m.Update(pressRune('a'))
	m = next.(AgentPreferredModel)
	for _, r := range "codex" {
		next, _ = m.Update(pressRune(r))
		m = next.(AgentPreferredModel)
	}
	if !m.adding || m.input != "codex" {
		t.Fatalf("adding=%v input=%q", m.adding, m.input)
	}
	next, _ = m.Update(pressKey("enter"))
	m = next.(AgentPreferredModel)
	if len(m.items) != 3 || m.items[2] != "codex" {
		t.Fatalf("append = %v", m.items)
	}
	next, _ = m.Update(pressRune('d'))
	m = next.(AgentPreferredModel)
	if len(m.items) != 2 || containsString(m.items, "codex") {
		t.Fatalf("delete = %v", m.items)
	}
	next, _ = m.Update(pressKey("enter"))
	m = next.(AgentPreferredModel)
	if !m.saved {
		t.Fatalf("not saved: %v", m.err)
	}
	if got := cfg.Agents.Preferred; len(got) != 2 || got[0] != "grok" || got[1] != "claude" {
		t.Fatalf("saved = %v", got)
	}
}
