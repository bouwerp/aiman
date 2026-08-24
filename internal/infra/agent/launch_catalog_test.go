package agent

import "testing"

func TestLaunchCatalogForClaudeHasModelsAndEffort(t *testing.T) {
	c := LaunchCatalogFor("claude")
	if !contains(c.Models, "sonnet") || !contains(c.Efforts, "high") {
		t.Fatalf("%+v", c)
	}
	if c.EffortFlag != "--effort" || !c.SupportsEffort() {
		t.Fatalf("%+v", c)
	}
	if !LaunchCatalogFor("claude-code").SupportsEffort() {
		t.Fatal("claude-code alias")
	}
}

func TestLaunchCatalogForGrok(t *testing.T) {
	c := LaunchCatalogFor("grok")
	if !contains(c.Models, "grok-4.6") || c.EffortFlag != "--reasoning-effort" {
		t.Fatalf("%+v", c)
	}
}

func TestLaunchCatalogForAgyEffort(t *testing.T) {
	c := LaunchCatalogFor("agy")
	if !c.SupportsEffort() || !contains(c.Models, "gemini-3.7-flash-high") {
		t.Fatalf("%+v", c)
	}
}

func TestLaunchCatalogEffortNA(t *testing.T) {
	if LaunchCatalogFor("opencode").SupportsEffort() {
		t.Fatal("opencode has no effort flag")
	}
	if LaunchCatalogFor("cursor-agent").SupportsEffort() {
		t.Fatal("cursor effort is in the model id")
	}
	if LaunchCatalogFor("ageni").SupportsEffort() || len(LaunchCatalogFor("ageni").Models) != 0 {
		t.Fatal("unknown agent has empty catalog")
	}
}

func TestLaunchCatalogCursorModelsFromCLI(t *testing.T) {
	c := LaunchCatalogFor("cursor-agent")
	if !contains(c.Models, "auto") || !contains(c.Models, "composer-2.5") {
		t.Fatalf("len=%d", len(c.Models))
	}
	if len(c.Models) < 50 {
		t.Fatalf("expected full --list-models snapshot, got %d", len(c.Models))
	}
}

func TestLaunchCatalogPiThinking(t *testing.T) {
	c := LaunchCatalogFor("pi")
	if c.EffortFlag != "--thinking" || !contains(c.Efforts, "xhigh") {
		t.Fatalf("%+v", c)
	}
}

func TestLaunchCatalogCodexConfig(t *testing.T) {
	c := LaunchCatalogFor("codex")
	if c.EffortConfig != "model_reasoning_effort" || c.EffortFlag != "" {
		t.Fatalf("%+v", c)
	}
}

func TestCommandBase(t *testing.T) {
	if got := CommandBase("claude --dangerously-skip-permissions"); got != "claude" {
		t.Fatalf("%q", got)
	}
	if CommandBase("  ") != "" {
		t.Fatal("empty")
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
