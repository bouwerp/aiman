package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadConfigText(t *testing.T, text string) *Config {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	aimanDir := filepath.Join(dir, DirName)
	if err := os.MkdirAll(aimanDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(aimanDir, ConfigName), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func TestLoadWarnsOnSessionBackend(t *testing.T) {
	cfg := loadConfigText(t, "remotes:\n  - name: devbox\n    host: devbox.local\n    session_backend: tmux\n  - name: other\n    host: other.local\n")
	if len(cfg.Warnings) != 1 {
		t.Fatalf("Warnings = %q, want one", cfg.Warnings)
	}
	for _, part := range []string{"devbox", "session_backend"} {
		if !strings.Contains(cfg.Warnings[0], part) {
			t.Errorf("warning %q does not mention %q", cfg.Warnings[0], part)
		}
	}
}

func TestLoadNoWarningWithoutSessionBackend(t *testing.T) {
	cfg := loadConfigText(t, "remotes:\n  - name: devbox\n    host: devbox.local\n")
	if len(cfg.Warnings) != 0 {
		t.Errorf("Warnings = %q, want none", cfg.Warnings)
	}
}
