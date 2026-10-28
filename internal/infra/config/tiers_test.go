package config

import (
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestValidTier(t *testing.T) {
	for name, want := range map[string]bool{TierSmart: true, TierWorker: true, "": false, "Smart": false, "cheap": false} {
		if got := ValidTier(name); got != want {
			t.Errorf("ValidTier(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestAgentsTierAndSetTier(t *testing.T) {
	var a AgentsConfig
	a.SetTier(TierSmart, []PreferredAgent{{Agent: "claude", Model: "opus"}})
	a.SetTier(TierWorker, PreferredFromCommands("pi"))
	if !reflect.DeepEqual(a.Tier(TierSmart), a.Smart) || len(a.Smart) != 1 {
		t.Fatalf("smart = %+v", a.Smart)
	}
	if !reflect.DeepEqual(a.Tier(TierWorker), a.Preferred) || a.Preferred[0].Agent != "pi" {
		t.Fatalf("worker = %+v", a.Preferred)
	}
	if a.Tier("other") != nil {
		t.Fatalf("unknown tier = %+v", a.Tier("other"))
	}
	a.SetTier("other", PreferredFromCommands("x"))
	if len(a.Smart) != 1 || len(a.Preferred) != 1 {
		t.Fatalf("SetTier on an unknown tier changed %+v", a)
	}
}

func TestAgentsSmartRoundTrip(t *testing.T) {
	in := "agents:\n" +
		"    smart:\n" +
		"        - agent: pi\n" +
		"          model: openrouter/moonshotai/kimi-k3\n" +
		"          effort: high\n" +
		"        - claude\n" +
		"    preferred:\n" +
		"        - codex\n"
	var c agentsDoc
	if err := yaml.Unmarshal([]byte(in), &c); err != nil {
		t.Fatal(err)
	}
	want := []PreferredAgent{{Agent: "pi", Model: "openrouter/moonshotai/kimi-k3", Effort: "high"}, {Agent: "claude"}}
	if !reflect.DeepEqual(c.Agents.Smart, want) {
		t.Fatalf("smart = %+v", c.Agents.Smart)
	}
	out, err := yaml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var back agentsDoc
	if err := yaml.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, c) {
		t.Fatalf("round trip = %+v\n%s", back, out)
	}
}

// A block with only smart set is not zero, and a smart list the user cleared
// is written back as smart: [].
func TestAgentsSmartIsZeroAndMarshal(t *testing.T) {
	if (AgentsConfig{Smart: []PreferredAgent{}}).IsZero() {
		t.Fatal("cleared smart list must not be zero")
	}
	out, err := yaml.Marshal(agentsDoc{Agents: AgentsConfig{Smart: []PreferredAgent{}}})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "agents:\n    smart: []\n" {
		t.Fatalf("marshal = %q", out)
	}
	out, err = yaml.Marshal(agentsDoc{Agents: AgentsConfig{Preferred: PreferredFromCommands("claude")}})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "agents:\n    preferred:\n        - claude\n" {
		t.Fatalf("marshal without smart = %q", out)
	}
}

func TestMarshalServeConfigCarriesSmart(t *testing.T) {
	cfg := &Config{Agents: AgentsConfig{Smart: []PreferredAgent{{Agent: "claude", Model: "opus", Effort: "high"}}}}
	body, err := cfg.MarshalServeConfig("h")
	if err != nil {
		t.Fatal(err)
	}
	var got ServeConfig
	if err := yaml.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Agents.Smart, cfg.Agents.Smart) {
		t.Fatalf("smart = %v\n%s", got.Agents.Smart, body)
	}
}
