package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLimitsRecordClaudeWritesTheFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	in := `{"cwd":"/secret-project","rate_limits":{"five_hour":{"used_percentage":41}}}`
	if err := runLimitsRecordClaude(strings.NewReader(in)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".aiman/limits/claude-statusline.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-project") || !strings.Contains(string(raw), "rate_limits") {
		t.Fatalf("stored %s", raw)
	}
}

func TestLimitsRecordClaudeBadInputReturnsNil(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := runLimitsRecordClaude(strings.NewReader("{nope")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".aiman/limits/claude-statusline.json")); !os.IsNotExist(err) {
		t.Fatalf("bad input wrote a file: %v", err)
	}
}

func TestLimitsRecordClaudeOversizedInputWritesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	in := `{"pad":"` + strings.Repeat("x", limitsRecordMaxInput) + `","rate_limits":{"five_hour":{"used_percentage":41}}}`
	if err := runLimitsRecordClaude(strings.NewReader(in)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".aiman/limits/claude-statusline.json")); !os.IsNotExist(err) {
		t.Fatalf("truncated input wrote a file: %v", err)
	}
}
