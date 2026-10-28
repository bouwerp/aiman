package config

import (
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

type agentsDoc struct {
	Agents AgentsConfig `yaml:"agents"`
}

func TestPreferredStringEntriesRoundTripByteIdentical(t *testing.T) {
	in := "agents:\n    preferred:\n        - claude\n        - codex\n"
	var c agentsDoc
	if err := yaml.Unmarshal([]byte(in), &c); err != nil {
		t.Fatal(err)
	}
	want := []PreferredAgent{{Agent: "claude"}, {Agent: "codex"}}
	if !reflect.DeepEqual(c.Agents.Preferred, want) {
		t.Fatalf("preferred = %+v", c.Agents.Preferred)
	}
	out, err := yaml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != in {
		t.Fatalf("round trip changed bytes:\n%s", out)
	}
}

func TestPreferredObjectAndMixedEntries(t *testing.T) {
	in := "agents:\n" +
		"    preferred:\n" +
		"        - claude\n" +
		"        - agent: pi\n" +
		"          model: openrouter/qwen/qwen3-coder\n" +
		"          effort: high\n" +
		"        - agent: pi\n" +
		"          model: deepseek/deepseek-flash\n"
	var c agentsDoc
	if err := yaml.Unmarshal([]byte(in), &c); err != nil {
		t.Fatal(err)
	}
	want := []PreferredAgent{
		{Agent: "claude"},
		{Agent: "pi", Model: "openrouter/qwen/qwen3-coder", Effort: "high"},
		{Agent: "pi", Model: "deepseek/deepseek-flash"},
	}
	if !reflect.DeepEqual(c.Agents.Preferred, want) {
		t.Fatalf("preferred = %+v", c.Agents.Preferred)
	}
	out, err := yaml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != in {
		t.Fatalf("round trip changed bytes:\n%s", out)
	}
	if got := c.Agents.PreferredCommands(); !reflect.DeepEqual(got, []string{"claude", "pi", "pi"}) {
		t.Fatalf("commands = %v", got)
	}
}

func TestPreferredEntryWithOnlyAgentKeyMarshalsAsString(t *testing.T) {
	var c agentsDoc
	if err := yaml.Unmarshal([]byte("agents:\n  preferred:\n    - agent: grok\n"), &c); err != nil {
		t.Fatal(err)
	}
	out, err := yaml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "agents:\n    preferred:\n        - grok\n" {
		t.Fatalf("out:\n%s", out)
	}
}

func TestPreferredRejectsUnknownShape(t *testing.T) {
	var c agentsDoc
	if err := yaml.Unmarshal([]byte("agents:\n  preferred:\n    - [pi, x]\n"), &c); err == nil {
		t.Fatal("a sequence entry must be rejected")
	}
}

func TestPreferredAgentLabel(t *testing.T) {
	cases := map[PreferredAgent]string{
		{Agent: "claude"}: "claude",
		{Agent: "pi", Model: "openrouter/qwen/qwen3-coder", Effort: "high"}: "pi  openrouter/qwen/qwen3-coder  high",
		{Agent: "codex", Effort: "low"}:                                     "codex  (default model)  low",
		{Agent: "pi", Model: "deepseek/deepseek-flash"}:                     "pi  deepseek/deepseek-flash",
	}
	for in, want := range cases {
		if got := in.Label(); got != want {
			t.Errorf("Label(%+v) = %q, want %q", in, got, want)
		}
	}
}
