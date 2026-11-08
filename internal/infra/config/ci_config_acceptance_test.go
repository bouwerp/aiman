package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// loadConfigFile writes body as the config file under a temporary HOME and
// loads it the way the laptop and serve do.
func loadConfigFile(t *testing.T, body string) *Config {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, DirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ConfigName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func TestCITrustedLoginsLoad(t *testing.T) {
	cfg := loadConfigFile(t, "ci:\n  trusted_logins: [review-bot, alice]\n")
	want := []string{"review-bot", "alice"}
	if !reflect.DeepEqual(cfg.CI.TrustedLogins, want) {
		t.Fatalf("CI.TrustedLogins = %q, want %q", cfg.CI.TrustedLogins, want)
	}
}

func TestMarshalServeConfigCarriesTrustedLogins(t *testing.T) {
	cfg := loadConfigFile(t, "ci:\n  trusted_logins: [review-bot, alice]\n")
	body, err := cfg.MarshalServeConfig("h1")
	if err != nil {
		t.Fatalf("MarshalServeConfig: %v", err)
	}
	if !strings.Contains(string(body), "trusted_logins") {
		t.Fatalf("serve config has no trusted_logins:\n%s", body)
	}
	var serve ServeConfig
	if err := yaml.Unmarshal(body, &serve); err != nil {
		t.Fatalf("unmarshal serve config: %v", err)
	}
	want := []string{"review-bot", "alice"}
	if !reflect.DeepEqual(serve.CI.TrustedLogins, want) {
		t.Fatalf("serve CI.TrustedLogins = %q, want %q\n%s", serve.CI.TrustedLogins, want, body)
	}
}

func TestMarshalServeConfigOmitsEmptyCI(t *testing.T) {
	cfg := loadConfigFile(t, "git:\n  include_personal: true\n")
	body, err := cfg.MarshalServeConfig("h1")
	if err != nil {
		t.Fatalf("MarshalServeConfig: %v", err)
	}
	var top map[string]any
	if err := yaml.Unmarshal(body, &top); err != nil {
		t.Fatalf("unmarshal serve config: %v", err)
	}
	if _, ok := top["ci"]; ok {
		t.Fatalf("serve config without a ci block marshals a ci key:\n%s", body)
	}
	if cfg.CI.TrustedLogins != nil {
		t.Fatalf("CI.TrustedLogins = %q, want none", cfg.CI.TrustedLogins)
	}
}
