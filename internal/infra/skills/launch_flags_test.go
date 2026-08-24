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
}
