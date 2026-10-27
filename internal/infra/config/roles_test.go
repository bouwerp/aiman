package config

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestResolveLaunchDefaultsPrecedence(t *testing.T) {
	cfg := &Config{
		AgentDefaults: map[string]AgentDefaults{"claude": {Model: "sonnet", Effort: "low"}},
		Repos: map[string]RepoBootstrap{"o/r": {
			AgentOverrides: map[string]AgentDefaults{"claude": {Effort: "high"}},
		}},
		Roles: RolesConfig{Verifier: RoleOverride{Model: "opus"}},
	}

	if got := cfg.ResolveLaunchDefaults("Claude Code", "claude", "o/r", ""); got != (AgentDefaults{Model: "sonnet", Effort: "high"}) {
		t.Fatalf("worker = %+v, want sonnet/high", got)
	}
	if got := cfg.ResolveLaunchDefaults("Claude Code", "claude", "O/R", "reviewer"); got != (AgentDefaults{Model: "opus", Effort: "high"}) {
		t.Fatalf("reviewer = %+v, want opus/high", got)
	}
	for _, role := range []string{"planner", "worker", "verifier"} {
		if got := cfg.ResolveLaunchDefaults("Claude Code", "claude", "o/r", role); got != (AgentDefaults{Model: "sonnet", Effort: "high"}) {
			t.Fatalf("role %q = %+v, want sonnet/high", role, got)
		}
	}

	cfg.Roles.Verifier = RoleOverride{Agent: "codex", Model: "o3"}
	if got := cfg.ResolveLaunchDefaults("Claude Code", "claude --flag", "o/r", "reviewer"); got != (AgentDefaults{Model: "sonnet", Effort: "high"}) {
		t.Fatalf("other agent's role = %+v, want sonnet/high", got)
	}
	if got := cfg.ResolveLaunchDefaults("Codex", "Codex", "o/r", "reviewer"); got.Model != "o3" {
		t.Fatalf("matching role agent = %+v, want model o3", got)
	}
	if got := cfg.ResolveLaunchDefaults("Claude Code", "claude", "x/y", ""); got != (AgentDefaults{Model: "sonnet", Effort: "low"}) {
		t.Fatalf("unknown repo = %+v, want sonnet/low", got)
	}
	if got := (*Config)(nil).ResolveLaunchDefaults("Claude Code", "claude", "o/r", "reviewer"); got != (AgentDefaults{}) {
		t.Fatalf("nil config = %+v", got)
	}
}

func TestResolveLaunchDefaultsMatchesOverrideByName(t *testing.T) {
	cfg := &Config{Repos: map[string]RepoBootstrap{"o/r": {
		AgentOverrides: map[string]AgentDefaults{"claude": {Model: "opus"}},
	}}}
	if got := cfg.ResolveLaunchDefaults("Claude Code", "/opt/bin/cc", "o/r", ""); got.Model != "opus" {
		t.Fatalf("name match = %+v, want opus", got)
	}
}

func TestRolesFor(t *testing.T) {
	r := RolesConfig{Verifier: RoleOverride{Agent: "codex", Model: "o3", Effort: "high"}}
	if got := r.For("reviewer"); got != r.Verifier {
		t.Fatalf("For(reviewer) = %+v", got)
	}
	for _, role := range []string{"planner", "", "worker"} {
		if got := r.For(role); got != (RoleOverride{}) {
			t.Fatalf("For(%q) = %+v, want zero", role, got)
		}
	}
	cfg := &Config{Roles: RolesConfig{Verifier: RoleOverride{Agent: " codex "}}}
	if got := cfg.RoleAgent("reviewer"); got != "codex" {
		t.Fatalf("RoleAgent(reviewer) = %q", got)
	}
	if got := cfg.RoleAgent("worker"); got != "" {
		t.Fatalf("RoleAgent(worker) = %q", got)
	}
	if got := (*Config)(nil).RoleAgent("reviewer"); got != "" {
		t.Fatalf("nil RoleAgent = %q", got)
	}
}

func TestReviewerCandidates(t *testing.T) {
	cfg := &Config{
		Roles:  RolesConfig{Verifier: RoleOverride{Agent: "codex"}},
		Verify: VerifyConfig{Agents: []string{"claude", "codex"}},
	}
	if got := cfg.ReviewerCandidates(); !reflect.DeepEqual(got, []string{"codex", "claude"}) {
		t.Fatalf("with role = %v", got)
	}
	cfg.Roles = RolesConfig{}
	if got := cfg.ReviewerCandidates(); !reflect.DeepEqual(got, []string{"claude", "codex"}) {
		t.Fatalf("without role = %v", got)
	}
	if got := (*Config)(nil).ReviewerCandidates(); len(got) != 0 {
		t.Fatalf("nil config = %v", got)
	}
}

func TestMarshalServeConfigCarriesRolesAndRepoKit(t *testing.T) {
	cfg := &Config{
		Roles: RolesConfig{Verifier: RoleOverride{Agent: "codex", Effort: "high"}},
		Repos: map[string]RepoBootstrap{"o/r": {
			DoneWhen:       []string{"`make ci` green", "PR open"},
			Requires:       []string{"gh", "mcp:atlassian"},
			AgentOverrides: map[string]AgentDefaults{"claude": {Model: "opus", Effort: "high"}},
		}},
		Integrations: Integrations{Jira: JiraConfig{AcceptanceCriteriaField: "customfield_10035"}},
	}
	body, err := cfg.MarshalServeConfig("h")
	if err != nil {
		t.Fatalf("MarshalServeConfig: %v", err)
	}
	for _, banned := range []string{"planner", "worker"} {
		if strings.Contains(string(body), banned) {
			t.Fatalf("serve config has %q key:\n%s", banned, body)
		}
	}
	var got ServeConfig
	if err := yaml.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Roles != cfg.Roles {
		t.Fatalf("roles = %+v", got.Roles)
	}
	if !reflect.DeepEqual(got.Repos["o/r"], cfg.Repos["o/r"]) {
		t.Fatalf("repo = %+v", got.Repos["o/r"])
	}
	if got.Integrations.Jira.AcceptanceCriteriaField != "customfield_10035" {
		t.Fatalf("jira = %+v", got.Integrations.Jira)
	}
}

func TestRepoKitYAMLShape(t *testing.T) {
	src := "repos:\n" +
		"  owner/name:\n" +
		"    done_when: [\"`make ci` green\", \"tests added\", \"PR open\"]\n" +
		"    requires: [gh, make, \"mcp:atlassian\"]\n" +
		"    agent_overrides:\n" +
		"      claude: {model: opus, effort: high}\n" +
		"roles:\n" +
		"  verifier: {agent: codex, model: \"\", effort: high}\n" +
		"integrations:\n" +
		"  jira:\n" +
		"    acceptance_criteria_field: customfield_10035\n"
	var cfg Config
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	r := cfg.Repos["owner/name"]
	if len(r.DoneWhen) != 3 || r.DoneWhen[0] != "`make ci` green" || len(r.Requires) != 3 ||
		r.AgentOverrides["claude"] != (AgentDefaults{Model: "opus", Effort: "high"}) {
		t.Fatalf("repo = %+v", r)
	}
	if cfg.Roles.Verifier != (RoleOverride{Agent: "codex", Effort: "high"}) {
		t.Fatalf("roles = %+v", cfg.Roles)
	}
	if cfg.Integrations.Jira.AcceptanceCriteriaField != "customfield_10035" {
		t.Fatalf("jira = %+v", cfg.Integrations.Jira)
	}
}
