package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

func TestPreferredEditorReordersAndSaves(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{
		AgentDefaults: map[string]config.AgentDefaults{"claude": {Model: "sonnet"}},
		Agents:        config.AgentsConfig{Preferred: []string{"claude", "grok"}},
	}
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
	if cfg.AgentDefaults["claude"].Model != "sonnet" {
		t.Fatal("agent_defaults was modified")
	}
	raw, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".aiman", "config.yaml"))
	if err != nil || !strings.Contains(string(raw), "preferred:") {
		t.Fatalf("config missing preferred: %v\n%s", err, raw)
	}
}

func TestPreferredEscInAddStaysOnPage(t *testing.T) {
	m, _ := promptModel(domainSessionForPreferred())
	m.state = viewStateMenu
	m.agentPreferred = NewAgentPreferredModel(m.cfg)
	m.agentPreferred.adding = true
	m.agentPreferred.input = "codex"
	m.state = viewStateAgentPreferred
	next, _ := m.handleAgentPreferredUpdate(pressKey("esc"))
	m = next.(*Model)
	if m.state != viewStateAgentPreferred {
		t.Fatalf("state = %v, want the editor", m.state)
	}
	if m.agentPreferred.adding {
		t.Fatal("esc did not cancel add mode")
	}
}

func TestPreferredEmptyListWritesKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{Agents: config.AgentsConfig{Preferred: []string{}}}
	body, err := cfg.MarshalServeConfig("h")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "preferred: []") {
		t.Fatalf("empty preferred was omitted:\n%s", body)
	}
}

func domainSessionForPreferred() domain.Session {
	return domain.Session{ID: "p", RemoteHost: "regent0"}
}

var _ tea.Model = AgentPreferredModel{}

func TestPreferredClearedListRoundTrips(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{Agents: config.AgentsConfig{Preferred: []string{"claude"}}}
	m := NewAgentPreferredModel(cfg)
	next, _ := m.Update(pressRune('d'))
	m = next.(AgentPreferredModel)
	next, _ = m.Update(pressKey("enter"))
	m = next.(AgentPreferredModel)
	if !m.saved {
		t.Fatalf("not saved: %v", m.err)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Agents.Preferred == nil || len(loaded.Agents.Preferred) != 0 {
		t.Fatalf("cleared preferred reloaded as %#v", loaded.Agents.Preferred)
	}
}
