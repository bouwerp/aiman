package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestUniqueRemotes(t *testing.T) {
	in := []Remote{
		{Name: "a", Host: "h1", User: "u", Root: "/r"},
		{Name: "b", Host: "h1", User: "u", Root: "/r"},
		{Name: "c", Host: "h2", User: "u", Root: "/r"},
		{Name: "", Host: "", User: "", Root: ""},
	}
	got := UniqueRemotes(in)
	want := []Remote{
		{Name: "a", Host: "h1", User: "u", Root: "/r"},
		{Name: "c", Host: "h2", User: "u", Root: "/r"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

// Save must create ~/.aiman itself rather than silently failing: it is
// called from many UI flows, some of which may run before EnsureDir ever
// has. This was also masking a test-isolation bug elsewhere — a test that
// forgot to sandbox HOME would rely on the developer's real ~/.aiman
// already existing, and once it did, Save wrote real config data into it.
func TestSaveCreatesMissingConfigDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &Config{Remotes: []Remote{{Name: "r1", Host: "h1"}}}

	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reloaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(reloaded.Remotes) != 1 || reloaded.Remotes[0].Host != "h1" {
		t.Fatalf("expected the saved remote to round-trip, got %+v", reloaded.Remotes)
	}
}

// A real save failure (the config directory's path is blocked by a plain
// file, not just missing) must still surface as an error.
func TestSaveFailsWhenConfigDirCannotBeCreated(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, DirName), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("failed to set up blocked config dir: %v", err)
	}
	t.Setenv("HOME", home)

	if err := (&Config{}).Save(); err == nil {
		t.Fatal("expected Save to fail when the config directory path is blocked")
	}
}

func TestMarshalServeConfigIncludesServiceSettingsWithoutRemotePaths(t *testing.T) {
	off := false
	cfg := &Config{
		Integrations: Integrations{Jira: JiraConfig{
			URL:              "https://jira.example.test",
			Email:            "user@example.test",
			APIToken:         "test-token",
			IssueStatuses:    []string{"Dev Ready"},
			TransitionStatus: "In Development",
		}},
		Git:           GitConfig{IncludeOrgs: []string{"acme"}},
		Skills:        SkillsConfig{Repo: "git@example.test:skills.git"},
		AgentDefaults: map[string]AgentDefaults{"codex": {Model: "gpt-5", Effort: "high"}},
		Gateway:       GatewayConfig{AllowLogins: []string{"user@example.test"}},
		Remotes:       []Remote{{Host: "remote.example.test", Root: "/repos", Notifications: &NotificationsConfig{SlackChannel: "#ops", Phone: &off}}},
		Notifications: NotificationsConfig{Webhook: "https://ntfy.sh/global"},
	}

	body, err := cfg.MarshalServeConfig("remote.example.test")
	if err != nil {
		t.Fatalf("MarshalServeConfig: %v", err)
	}

	var got map[string]any
	if err := yaml.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal serve config: %v", err)
	}
	if _, ok := got["remotes"]; ok {
		t.Fatalf("serve config must not carry local remote paths: %s", body)
	}
	if got["integrations"] == nil || got["agent_defaults"] == nil || got["skills"] == nil {
		t.Fatalf("serve config missing required settings: %s", body)
	}
	if got["gateway"] == nil {
		t.Fatalf("serve config missing gateway settings: %s", body)
	}
	notif, ok := got["notifications"].(map[string]any)
	if !ok {
		t.Fatalf("notifications missing: %s", body)
	}
	if notif["slack_channel"] != "#ops" {
		t.Fatalf("notifications = %#v", notif)
	}
	if notif["webhook"] != "https://ntfy.sh/global" {
		t.Fatalf("notifications = %#v", notif)
	}
	if notif["phone"] != false {
		t.Fatalf("notifications = %#v", notif)
	}
}

func TestUsagePricesRoundTrip(t *testing.T) {
	const raw = `
usage:
  prices:
    gpt-5.6-terra:
      input: 1.5
      output: 12
      cache_read: 0.15
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, ok := cfg.Usage.Prices["gpt-5.6-terra"]
	if !ok {
		t.Fatalf("missing gpt-5.6-terra: %#v", cfg.Usage)
	}
	if got.Output != 12 {
		t.Fatalf("Output = %v, want 12", got.Output)
	}
	if got.Input != 1.5 || got.CacheRead != 0.15 {
		t.Fatalf("price = %+v, want input 1.5 cache_read 0.15", got)
	}
	if got.CacheWrite5m != 0 || got.CacheWrite1h != 0 {
		t.Fatalf("omitted cache-write fields = %v %v, want 0", got.CacheWrite5m, got.CacheWrite1h)
	}
}

func TestContextWindowOmittedIsZero(t *testing.T) {
	raw, err := (&Config{}).MarshalServeConfig("x")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "context_window_tokens") {
		t.Fatalf("omitted key was written:\n%s", raw)
	}
	cfg := &Config{}
	cfg.Usage.ContextWindowTokens = 200_000
	raw, err = cfg.MarshalServeConfig("r")
	if err != nil {
		t.Fatal(err)
	}
	var loaded Config
	if err := yaml.Unmarshal(raw, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.Usage.ContextWindowTokens != 200_000 {
		t.Fatalf("ContextWindowTokens = %d", loaded.Usage.ContextWindowTokens)
	}
}

func TestMarshalServeConfigCarriesUsage(t *testing.T) {
	cfg := &Config{
		Usage: UsageConfig{
			Prices: map[string]ModelPrice{
				"gpt-5.6-terra": {Input: 1.5, Output: 12, CacheRead: 0.15},
			},
		},
	}
	body, err := cfg.MarshalServeConfig("r")
	if err != nil {
		t.Fatalf("MarshalServeConfig: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "usage:") || !strings.Contains(text, "gpt-5.6-terra:") {
		t.Fatalf("serve config missing usage prices:\n%s", text)
	}
}

func TestGatewayFunnelPermittedDefaultsOn(t *testing.T) {
	if !(*Config)(nil).GatewayFunnelPermitted() {
		t.Fatal("nil config must permit funnel")
	}
	if !(&Config{}).GatewayFunnelPermitted() {
		t.Fatal("absent funnel key must permit funnel")
	}
	off := false
	if (&Config{Gateway: GatewayConfig{Funnel: &off}}).GatewayFunnelPermitted() {
		t.Fatal("funnel: false must refuse --funnel")
	}
	on := true
	if !(&Config{Gateway: GatewayConfig{Funnel: &on}}).GatewayFunnelPermitted() {
		t.Fatal("funnel: true must permit --funnel")
	}
}

func TestMarshalServeConfigCarriesVerify(t *testing.T) {
	cfg := &Config{Verify: VerifyConfig{Agents: []string{"codex", "claude"}, BeforePR: true, Timeout: 20 * time.Minute}}
	body, err := cfg.MarshalServeConfig("h")
	if err != nil {
		t.Fatalf("MarshalServeConfig: %v", err)
	}
	var got ServeConfig
	if err := yaml.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Verify, cfg.Verify) {
		t.Fatalf("verify = %+v, want %+v\n%s", got.Verify, cfg.Verify, body)
	}
}

func TestMarshalServeConfigCarriesPreferred(t *testing.T) {
	cfg := &Config{Agents: AgentsConfig{Preferred: []PreferredAgent{{Agent: "claude"}, {Agent: "pi", Model: "openrouter/qwen/qwen3-coder", Effort: "high"}}}}
	body, err := cfg.MarshalServeConfig("h")
	if err != nil {
		t.Fatal(err)
	}
	var got ServeConfig
	if err := yaml.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Agents.Preferred, cfg.Agents.Preferred) {
		t.Fatalf("preferred = %v\n%s", got.Agents.Preferred, body)
	}
	var omitted ServeConfig
	if err := yaml.Unmarshal([]byte("{}"), &omitted); err != nil {
		t.Fatal(err)
	}
	if omitted.Agents.Preferred != nil {
		t.Fatalf("omitted preferred = %v", omitted.Agents.Preferred)
	}
}
