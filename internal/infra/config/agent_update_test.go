package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestAgentUpdateGuardsDefaultOn(t *testing.T) {
	var nilCfg *Config
	if !nilCfg.AgentUpdateGuards() || !(&Config{}).AgentUpdateGuards() {
		t.Fatal("guards must default on")
	}
	for mode, want := range map[string]bool{"auto": true, "notify": true, "off": false, "OFF": false} {
		cfg := &Config{Agents: AgentsConfig{Update: AgentUpdateConfig{Mode: mode}}}
		if got := cfg.AgentUpdateGuards(); got != want {
			t.Errorf("mode %q: guards = %v, want %v", mode, got, want)
		}
	}
}

func TestAgentsUpdateRoundTrips(t *testing.T) {
	if !(AgentsConfig{}).IsZero() {
		t.Fatal("an empty agents block must be zero")
	}
	if (AgentsConfig{Update: AgentUpdateConfig{Mode: "off"}}).IsZero() {
		t.Fatal("an update mode alone must keep the agents block")
	}
	var cfg Config
	if err := yaml.Unmarshal([]byte("agents:\n  update:\n    mode: off\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.AgentUpdateGuards() {
		t.Fatal("mode off from yaml must turn guards off")
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "mode: \"off\"") && !strings.Contains(string(out), "mode: off") {
		t.Fatalf("mode lost on write:\n%s", out)
	}
}
