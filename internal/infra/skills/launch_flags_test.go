package skills

import (
	"context"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

func TestLaunchFlagsUseRepoAndRole(t *testing.T) {
	cfg := &config.Config{
		AgentDefaults: map[string]config.AgentDefaults{"claude": {Model: "sonnet"}},
		Repos: map[string]config.RepoBootstrap{
			"o/r": {AgentOverrides: map[string]config.AgentDefaults{"claude": {Model: "opus"}}},
		},
		Roles: config.RolesConfig{Verifier: config.RoleOverride{Effort: "high"}},
	}
	ag := domain.Agent{Name: "Claude Code", Command: "claude"}
	if got := applyConfiguredLaunchFlags("claude", ag, cfg, "o/r", ""); !strings.Contains(got, "--model opus") {
		t.Fatalf("repo override: %q", got)
	}
	if got := applyConfiguredLaunchFlags("claude", ag, cfg, "x/y", ""); !strings.Contains(got, "--model sonnet") {
		t.Fatalf("agent default elsewhere: %q", got)
	}
	got := applyConfiguredLaunchFlags("claude", ag, cfg, "o/r", string(domain.SessionRoleReviewer))
	if !strings.Contains(got, "--model opus") || !strings.Contains(got, "--effort high") {
		t.Fatalf("reviewer role: %q", got)
	}

	res, err := NewEngine(cfg).PrepareSession(context.Background(), newMockRemote(), "/wt", ag, nil, true, nil, nil, domain.QualityKit{Repo: "o/r"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Command, "--model opus") {
		t.Fatalf("PrepareSession must apply the repo override: %q", res.Command)
	}
}

func TestLaunchFlagsHandoverRoleUsesCheap(t *testing.T) {
	handover := string(domain.SessionRoleHandover)
	claude := domain.Agent{Name: "Claude Code", Command: "claude"}
	cases := []struct {
		name    string
		cfg     *config.Config
		ag      domain.Agent
		role    string
		want    []string
		notWant []string
	}{
		{
			name: "claude gets haiku",
			cfg:  &config.Config{},
			ag:   claude, role: handover,
			want: []string{"--model haiku"},
		},
		{
			name: "role model beats Cheap",
			cfg:  &config.Config{Roles: config.RolesConfig{Handover: config.RoleOverride{Model: "sonnet", Effort: "low"}}},
			ag:   claude, role: handover,
			want:    []string{"--model sonnet", "--effort low"},
			notWant: []string{"haiku"},
		},
		{
			name: "Cheap beats agent_defaults and repo overrides",
			cfg: &config.Config{
				AgentDefaults: map[string]config.AgentDefaults{"claude": {Model: "opus", Effort: "high"}},
				Repos: map[string]config.RepoBootstrap{
					"o/r": {AgentOverrides: map[string]config.AgentDefaults{"claude": {Model: "fable"}}},
				},
			},
			ag: claude, role: handover,
			want:    []string{"--model haiku", "--effort high"},
			notWant: []string{"opus", "fable"},
		},
		{
			name: "role model for another agent does not block Cheap",
			cfg:  &config.Config{Roles: config.RolesConfig{Handover: config.RoleOverride{Agent: "codex", Model: "o3"}}},
			ag:   claude, role: handover,
			want:    []string{"--model haiku"},
			notWant: []string{"o3"},
		},
		{
			name: "codex gets its mini model",
			cfg:  &config.Config{},
			ag:   domain.Agent{Name: "Codex", Command: "codex"}, role: handover,
			want: []string{"--model gpt-5.4-mini"},
		},
		{
			name: "no Cheap means no flag",
			cfg:  &config.Config{},
			ag:   domain.Agent{Name: "Grok", Command: "grok"}, role: handover,
			notWant: []string{"--model"},
		},
		{
			name: "worker keeps agent_defaults",
			cfg:  &config.Config{AgentDefaults: map[string]config.AgentDefaults{"claude": {Model: "opus"}}},
			ag:   claude, role: "",
			want:    []string{"--model opus"},
			notWant: []string{"haiku"},
		},
		{
			name: "reviewer does not get Cheap",
			cfg:  &config.Config{},
			ag:   claude, role: string(domain.SessionRoleReviewer),
			notWant: []string{"--model"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := applyConfiguredLaunchFlags(tc.ag.Command, tc.ag, tc.cfg, "o/r", tc.role)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("%q lacks %q", got, w)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(got, w) {
					t.Errorf("%q must not contain %q", got, w)
				}
			}
		})
	}
}

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
	got = applyLaunchDefaults("\"$HOME/.aiman/hooks/deepcode-launch.sh\"", "deepcode", config.AgentDefaults{Model: "deepseek-v4-pro", Effort: "high"})
	if !strings.Contains(got, "DEEPCODE_MODEL=deepseek-v4-pro") || !strings.Contains(got, "DEEPCODE_REASONING_EFFORT=high") {
		t.Fatalf("deepcode defaults are env assignments: %q", got)
	}
	if strings.Contains(got, "--model") || strings.Contains(got, "--reasoning-effort") {
		t.Fatalf("deepcode rejects unknown flags: %q", got)
	}
	t.Setenv("DEEPCODE_MODEL", "from-env")
	got = applyLaunchDefaults("deepcode", "deepcode", config.AgentDefaults{Model: "deepseek-flash", Effort: "low"})
	if strings.Contains(got, "DEEPCODE_MODEL=") {
		t.Fatalf("process env wins over settings: %q", got)
	}
	if !strings.Contains(got, "DEEPCODE_REASONING_EFFORT=low") {
		t.Fatalf("effort still applies: %q", got)
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
		{"muse", "muse", "--yolo"},
		{"deepcode", "deepcode", "deepcode-launch.sh"},
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
