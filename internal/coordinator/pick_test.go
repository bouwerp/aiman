package coordinator

import (
	"errors"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/config"
)

func TestPickAgentSkipsMissing(t *testing.T) {
	got, err := PickAgent(config.PreferredFromCommands("codex", "claude"), []string{"claude", "grok"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent != "claude" {
		t.Fatalf("PickAgent = %+v, want claude", got)
	}
}

func TestPickAgentNoneInstalled(t *testing.T) {
	_, err := PickAgent(config.PreferredFromCommands("codex", "claude", "codex"), []string{"agy"})
	if !errors.Is(err, ErrNoPreferredAgent) {
		t.Fatalf("err = %v, want ErrNoPreferredAgent", err)
	}
	msg := err.Error()
	i, j := strings.Index(msg, "codex"), strings.Index(msg, "claude")
	if i < 0 || j < 0 || i > j {
		t.Fatalf("error %q must list codex then claude", msg)
	}
	if strings.Count(msg, "codex") != 1 {
		t.Fatalf("error %q repeats codex", msg)
	}
}

func TestPickAgentTrimsAndIsCaseSensitive(t *testing.T) {
	got, err := PickAgent(config.PreferredFromCommands(" ", " Claude", " claude "), []string{"claude"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent != "claude" {
		t.Fatalf("PickAgent = %+v, want claude", got)
	}
	if _, err := PickAgent(nil, []string{"claude"}); !errors.Is(err, ErrNoPreferredAgent) {
		t.Fatalf("empty list: err = %v, want ErrNoPreferredAgent", err)
	}
}

// The same agent may appear more than once; the first installed entry wins,
// and its model and effort come with it.
func TestPickAgentDuplicateAgentsKeepOrder(t *testing.T) {
	preferred := []config.PreferredAgent{
		{Agent: "codex", Model: "gpt-5", Effort: "high"},
		{Agent: "pi", Model: "openrouter/qwen/qwen3-coder", Effort: "high"},
		{Agent: "pi", Model: "deepseek/deepseek-flash"},
		{Agent: "claude"},
	}
	got, err := PickAgent(preferred, []string{"pi", "claude"})
	if err != nil {
		t.Fatal(err)
	}
	want := config.PreferredAgent{Agent: "pi", Model: "openrouter/qwen/qwen3-coder", Effort: "high"}
	if got != want {
		t.Fatalf("PickAgent = %+v, want %+v", got, want)
	}
}

func TestPickTier(t *testing.T) {
	agents := config.AgentsConfig{
		Smart:     []config.PreferredAgent{{Agent: "claude", Model: "opus", Effort: "high"}, {Agent: "pi", Model: "kimi"}},
		Preferred: []config.PreferredAgent{{Agent: "pi", Model: "glm"}, {Agent: "codex"}},
	}
	opus := config.PreferredAgent{Agent: "claude", Model: "opus", Effort: "high"}
	cases := []struct {
		name      string
		agents    config.AgentsConfig
		want      string
		installed []string
		pick      Pick
	}{
		{"smart", agents, config.TierSmart, []string{"claude", "pi"},
			Pick{Entry: opus, Tier: config.TierSmart, Requested: config.TierSmart}},
		{"worker", agents, config.TierWorker, []string{"claude", "pi"},
			Pick{Entry: config.PreferredAgent{Agent: "pi", Model: "glm"}, Tier: config.TierWorker, Requested: config.TierWorker}},
		{"empty request is worker", agents, "", []string{"codex"},
			Pick{Entry: config.PreferredAgent{Agent: "codex"}, Tier: config.TierWorker, Requested: config.TierWorker}},
		{"smart falls back to worker", agents, config.TierSmart, []string{"codex"},
			Pick{Entry: config.PreferredAgent{Agent: "codex"}, Tier: config.TierWorker, Requested: config.TierSmart, FellBack: true}},
		{"empty smart falls back to worker", config.AgentsConfig{Preferred: agents.Preferred}, config.TierSmart, []string{"pi"},
			Pick{Entry: config.PreferredAgent{Agent: "pi", Model: "glm"}, Tier: config.TierWorker, Requested: config.TierSmart, FellBack: true}},
		{"worker falls back to smart", agents, config.TierWorker, []string{"claude"},
			Pick{Entry: opus, Tier: config.TierSmart, Requested: config.TierWorker, FellBack: true}},
		{"empty worker falls back to smart", config.AgentsConfig{Smart: agents.Smart}, config.TierWorker, []string{"pi"},
			Pick{Entry: config.PreferredAgent{Agent: "pi", Model: "kimi"}, Tier: config.TierSmart, Requested: config.TierWorker, FellBack: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PickTier(tc.agents, tc.want, tc.installed)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.pick {
				t.Fatalf("PickTier = %+v, want %+v", got, tc.pick)
			}
		})
	}
}

func TestPickTierBothMissing(t *testing.T) {
	agents := config.AgentsConfig{
		Smart:     config.PreferredFromCommands("claude", "pi"),
		Preferred: config.PreferredFromCommands("codex"),
	}
	_, err := PickTier(agents, config.TierWorker, []string{"agy"})
	if !errors.Is(err, ErrNoPreferredAgent) {
		t.Fatalf("err = %v, want ErrNoPreferredAgent", err)
	}
	msg := err.Error()
	for _, want := range []string{"worker: tried codex", "smart: tried claude, pi"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q lacks %q", msg, want)
		}
	}
	_, err = PickTier(config.AgentsConfig{}, config.TierSmart, []string{"claude"})
	if !errors.Is(err, ErrNoPreferredAgent) || !strings.Contains(err.Error(), "smart: empty") || !strings.Contains(err.Error(), "worker: empty") {
		t.Fatalf("empty tiers: err = %v", err)
	}
}

func TestPickTierUnknownTier(t *testing.T) {
	_, err := PickTier(config.AgentsConfig{Preferred: config.PreferredFromCommands("claude")}, "cheap", []string{"claude"})
	if err == nil || errors.Is(err, ErrNoPreferredAgent) {
		t.Fatalf("err = %v, want an unknown tier error", err)
	}
}
