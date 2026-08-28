package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

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
		Remotes:       []Remote{{Host: "remote.example.test", Root: "/repos"}},
	}

	body, err := cfg.MarshalServeConfig()
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
