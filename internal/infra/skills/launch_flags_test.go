package skills

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/config"
)

func TestApplyLaunchDefaults(t *testing.T) {
	got := applyLaunchDefaults("claude --dangerously-skip-permissions", "claude", config.AgentDefaults{Model: "sonnet", Effort: "medium"})
	if !strings.Contains(got, "--model sonnet") || !strings.Contains(got, "--effort medium") {
		t.Fatalf("%q", got)
	}
	got = applyLaunchDefaults("grok --always-approve", "grok", config.AgentDefaults{Model: "4.6", Effort: "medium"})
	if !strings.Contains(got, "--model 4.6") || !strings.Contains(got, "--reasoning-effort medium") {
		t.Fatalf("%q", got)
	}
	got = applyLaunchDefaults("codex", "codex", config.AgentDefaults{Model: "gpt-5.6", Effort: "medium"})
	if !strings.Contains(got, "--model gpt-5.6") || !strings.Contains(got, "model_reasoning_effort=medium") {
		t.Fatalf("%q", got)
	}
	got = applyLaunchDefaults("claude --model opus", "claude", config.AgentDefaults{Model: "sonnet"})
	if strings.Contains(got, "--model sonnet") {
		t.Fatalf("must not override existing --model: %q", got)
	}
	got = applyLaunchDefaults("agy --dangerously-skip-permissions", "agy", config.AgentDefaults{Effort: "high"})
	if !strings.Contains(got, "--effort high") {
		t.Fatalf("%q", got)
	}
	got = applyLaunchDefaults("pi", "pi", config.AgentDefaults{Effort: "high"})
	if !strings.Contains(got, "--thinking high") {
		t.Fatalf("%q", got)
	}
	got = applyLaunchDefaults("kilo", "kilo", config.AgentDefaults{Effort: "high"})
	if !strings.Contains(got, "--variant high") {
		t.Fatalf("kilo must apply --variant: %q", got)
	}
	got = applyLaunchDefaults("cursor-agent", "cursor-agent", config.AgentDefaults{Effort: "high"})
	if strings.Contains(got, "effort") || strings.Contains(got, "variant") {
		t.Fatalf("cursor-agent must ignore effort: %q", got)
	}
}

func TestEnsureInteractiveLaunch(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want string
	}{
		{"codex", "codex", "--disable in_app_updates"},
		{"kilo", "kilo", "--auto"},
		{"kilocode alias", "kilocode", "--auto"},
		{"grok", "grok", "--no-auto-update"},
		{"grok-build alias", "grok-build", "--no-auto-update"},
		{"cursor-agent", "cursor-agent", "--disable-auto-update"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := EnsureInteractiveLaunch(c.cmd, "/home/user/code/myrepo")
			if !strings.Contains(got, c.want) {
				t.Errorf("%s: expected %q in revived command, got: %s", c.name, c.want, got)
			}
		})
	}

	// An agent with no revival-time flags is passed through unchanged.
	if got := EnsureInteractiveLaunch("claude", "/home/user/code/myrepo"); got != "claude" {
		t.Errorf("expected claude command unchanged, got: %q", got)
	}
}
