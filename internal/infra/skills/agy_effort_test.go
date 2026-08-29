package skills

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/config"
)

// Every model in agy's catalog carries its reasoning effort in the name —
// gemini-3.7-flash-low, gpt-oss-120b-medium — and agy also accepts --effort.
// Applying both launched the agent asking for two different efforts at once.
func TestAgyEffortNotAppendedWhenBakedIntoTheModel(t *testing.T) {
	for _, model := range []string{"gemini-3.7-flash-low", "gemini-3.1-pro-high", "gpt-oss-120b-medium"} {
		got := applyLaunchDefaults("agy", "agy", config.AgentDefaults{Model: model, Effort: "high"})
		if strings.Contains(got, "--effort") {
			t.Errorf("model %s bakes in its effort, so --effort must be omitted: %q", model, got)
		}
		if !strings.Contains(got, "--model "+model) {
			t.Errorf("model flag lost: %q", got)
		}
	}
}

// A model without an effort suffix still needs the flag.
func TestAgyEffortAppliedWhenTheModelDoesNotCarryIt(t *testing.T) {
	got := applyLaunchDefaults("agy", "agy", config.AgentDefaults{Model: "claude-sonnet-4-6", Effort: "high"})
	if !strings.Contains(got, "--effort high") {
		t.Errorf("expected --effort, got %q", got)
	}
}

// The model can arrive on the command rather than from config.
func TestAgyEffortReadsAModelAlreadyOnTheCommand(t *testing.T) {
	got := applyLaunchDefaults("agy --model gemini-3.6-flash-low", "agy", config.AgentDefaults{Effort: "medium"})
	if strings.Contains(got, "--effort") {
		t.Errorf("effort is baked into the command's own model: %q", got)
	}
}

// Only agy names models this way; nothing else should lose its effort flag.
func TestOtherAgentsKeepTheirEffortFlag(t *testing.T) {
	got := applyLaunchDefaults("grok", "grok", config.AgentDefaults{Model: "some-model-high", Effort: "high"})
	if !strings.Contains(got, "high") {
		t.Errorf("a non-agy agent must keep its effort: %q", got)
	}
}

func TestResolvedModelPrefersTheConfiguredValue(t *testing.T) {
	if got := resolvedModel("agy --model from-cmd", "from-config"); got != "from-config" {
		t.Errorf("got %q", got)
	}
	if got := resolvedModel("agy --model from-cmd --effort low", ""); got != "from-cmd" {
		t.Errorf("got %q", got)
	}
	if got := resolvedModel("agy", ""); got != "" {
		t.Errorf("got %q", got)
	}
	if got := resolvedModel("agy --model", ""); got != "" {
		t.Errorf("a dangling --model has no value, got %q", got)
	}
}
