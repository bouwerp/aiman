package skills

import (
	"context"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// A child started from a preferred entry runs that entry's model and effort
// over agent_defaults and the repo's agent_overrides; a role override still
// wins over the entry.
func TestLaunchFlagsPreferredEntryOverridesDefaults(t *testing.T) {
	cfg := &config.Config{
		AgentDefaults: map[string]config.AgentDefaults{"pi": {Model: "deepseek/deepseek-flash", Effort: "low"}},
		Repos: map[string]config.RepoBootstrap{
			"o/r": {AgentOverrides: map[string]config.AgentDefaults{"pi": {Model: "openai/gpt-4o"}}},
		},
		Roles: config.RolesConfig{Verifier: config.RoleOverride{Model: "google/gemini-2.5-pro"}},
	}
	ag := domain.Agent{Name: "pi", Command: "pi", LaunchModel: "openrouter/qwen/qwen3-coder", LaunchEffort: "high"}
	got := applyConfiguredLaunchFlags("pi", ag, cfg, "o/r", "")
	if !strings.Contains(got, "--model openrouter/qwen/qwen3-coder") || !strings.Contains(got, "--thinking high") {
		t.Fatalf("preferred entry: %q", got)
	}
	if strings.Contains(got, "gpt-4o") || strings.Contains(got, "deepseek") || strings.Contains(got, "low") {
		t.Fatalf("defaults leaked into %q", got)
	}
	got = applyConfiguredLaunchFlags("pi", ag, cfg, "o/r", string(domain.SessionRoleReviewer))
	if !strings.Contains(got, "--model google/gemini-2.5-pro") || !strings.Contains(got, "--thinking high") {
		t.Fatalf("role over entry: %q", got)
	}
	if got := applyConfiguredLaunchFlags("pi", ag, nil, "", ""); !strings.Contains(got, "--model openrouter/qwen/qwen3-coder --thinking high") {
		t.Fatalf("no config: %q", got)
	}
	suffixed := domain.Agent{Name: "pi", Command: "pi", LaunchModel: "openrouter/qwen/qwen3-coder:medium", LaunchEffort: "high"}
	if got := applyConfiguredLaunchFlags("pi", suffixed, cfg, "", ""); strings.Contains(got, "--thinking") {
		t.Fatalf("a :level suffix carries the thinking level: %q", got)
	}

	res, err := NewEngine(cfg).PrepareSession(context.Background(), newMockRemote(), "/wt", ag, nil, true, nil, nil, domain.QualityKit{Repo: "o/r"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Command, "--model openrouter/qwen/qwen3-coder") || !strings.Contains(res.Command, "--thinking high") {
		t.Fatalf("PrepareSession must apply the entry: %q", res.Command)
	}
}
