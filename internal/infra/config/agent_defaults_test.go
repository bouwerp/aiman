package config

import "testing"

func TestLaunchDefaultsFor(t *testing.T) {
	cfg := &Config{AgentDefaults: map[string]AgentDefaults{
		"claude": {Model: "sonnet", Effort: "medium"},
		"grok":   {Model: "4.6", Effort: "medium"},
	}}
	d := cfg.LaunchDefaultsFor("Claude Code", "claude --dangerously-skip-permissions")
	if d.Model != "sonnet" || d.Effort != "medium" {
		t.Fatalf("%+v", d)
	}
	d = cfg.LaunchDefaultsFor("Grok Build CLI", "grok")
	if d.Model != "4.6" {
		t.Fatalf("%+v", d)
	}
	if cfg.LaunchDefaultsFor("agy", "agy").Model != "" {
		t.Fatal("missing key")
	}
}

func TestAWSLocalProfileAllowed(t *testing.T) {
	cfg := &Config{}
	if !cfg.AWSLocalProfileAllowed("default") {
		t.Fatal("omitted list allows all")
	}
	none := []string{}
	cfg.AWS.IncludeProfiles = &none
	if cfg.AWSLocalProfileAllowed("default") {
		t.Fatal("empty list allows none")
	}
	only := []string{"work"}
	cfg.AWS.IncludeProfiles = &only
	if cfg.AWSLocalProfileAllowed("default") || !cfg.AWSLocalProfileAllowed("work") {
		t.Fatal("allowlist")
	}
}
