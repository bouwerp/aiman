package config

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestHandoverConfigDefaults(t *testing.T) {
	var h HandoverConfig
	if !h.ChildEnabled() {
		t.Fatal("child must default on")
	}
	if got := h.ChildTimeout(); got != 10*time.Minute {
		t.Fatalf("default timeout = %v", got)
	}
	off := false
	h = HandoverConfig{Child: &off, Timeout: 3 * time.Minute}
	if h.ChildEnabled() || h.ChildTimeout() != 3*time.Minute {
		t.Fatalf("explicit = %+v", h)
	}
}

func TestHandoverConfigYAMLRoundTrip(t *testing.T) {
	raw := "handover:\n  child: false\n  timeout: 5m\nroles:\n  handover:\n    agent: codex\n    model: o3\n    effort: low\n"
	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Handover.ChildEnabled() || cfg.Handover.Timeout != 5*time.Minute {
		t.Fatalf("handover = %+v", cfg.Handover)
	}
	if cfg.Roles.Handover != (RoleOverride{Agent: "codex", Model: "o3", Effort: "low"}) {
		t.Fatalf("roles.handover = %+v", cfg.Roles.Handover)
	}

	body, err := cfg.MarshalServeConfig("h")
	if err != nil {
		t.Fatal(err)
	}
	var back Config
	if err := yaml.Unmarshal(body, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.Handover, cfg.Handover) || back.Roles.Handover != cfg.Roles.Handover {
		t.Fatalf("serve round trip lost handover: %s", body)
	}

	var empty Config
	if err := yaml.Unmarshal([]byte("remotes: []\n"), &empty); err != nil {
		t.Fatal(err)
	}
	if !empty.Handover.ChildEnabled() {
		t.Fatal("missing handover block must leave the child on")
	}
	body, err = empty.MarshalServeConfig("h")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "handover") {
		t.Fatalf("empty handover must be omitted: %s", body)
	}
}

func TestHandoverCandidates(t *testing.T) {
	cfg := &Config{
		Roles: RolesConfig{Handover: RoleOverride{Agent: " codex ", Effort: "high"}},
		Agents: AgentsConfig{Preferred: []PreferredAgent{
			{Agent: "claude", Model: "sonnet"},
			{Agent: "codex"},
			{Agent: " "},
			{Agent: "agy"},
		}},
	}
	want := []PreferredAgent{
		{Agent: "codex", Effort: "high"},
		{Agent: "claude", Model: "sonnet"},
		{Agent: "codex"},
		{Agent: "agy"},
	}
	if got := cfg.HandoverCandidates("agy"); !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %+v, want %+v", got, want)
	}
	// The worker's own agent comes last, with no model.
	want = append(want, PreferredAgent{Agent: "pi"})
	if got := cfg.HandoverCandidates("pi"); !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %+v, want %+v", got, want)
	}
	if got, want := (&Config{}).HandoverCandidates("claude"), []PreferredAgent{{Agent: "claude"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("worker only = %+v, want %+v", got, want)
	}
	if got := (&Config{}).HandoverCandidates(""); len(got) != 0 {
		t.Fatalf("nothing configured = %+v", got)
	}
	if got, want := (*Config)(nil).HandoverCandidates("claude"), []PreferredAgent{{Agent: "claude"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("nil config = %+v, want %+v", got, want)
	}
}

func TestResolveLaunchDefaultsHandoverRole(t *testing.T) {
	cfg := &Config{
		AgentDefaults: map[string]AgentDefaults{"claude": {Model: "sonnet", Effort: "low"}},
		Roles: RolesConfig{
			Verifier: RoleOverride{Model: "opus"},
			Handover: RoleOverride{Effort: "medium"},
		},
	}
	if got := cfg.ResolveLaunchDefaults("Claude Code", "claude", "", "handover"); got != (AgentDefaults{Model: "sonnet", Effort: "medium"}) {
		t.Fatalf("handover = %+v, want sonnet/medium", got)
	}
	cfg.Roles.Handover = RoleOverride{Agent: "codex", Model: "o3"}
	if got := cfg.ResolveLaunchDefaults("Claude Code", "claude", "", "handover"); got != (AgentDefaults{Model: "sonnet", Effort: "low"}) {
		t.Fatalf("other agent's handover role = %+v", got)
	}
	if got := cfg.RoleOverrideFor("handover", "codex --x"); got.Model != "o3" {
		t.Fatalf("RoleOverrideFor match = %+v", got)
	}
	if got := cfg.RoleOverrideFor("handover", "claude"); got != (RoleOverride{}) {
		t.Fatalf("RoleOverrideFor other agent = %+v", got)
	}
	if got := cfg.RoleAgent("handover"); got != "codex" {
		t.Fatalf("RoleAgent(handover) = %q", got)
	}
	if got := cfg.Roles.For("handover"); got != cfg.Roles.Handover {
		t.Fatalf("For(handover) = %+v", got)
	}
}
