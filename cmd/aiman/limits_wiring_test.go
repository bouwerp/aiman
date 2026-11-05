package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// The status line script runs `aiman limits record-claude`, so that route must
// reach the recorder without needing a serve socket.
func TestLimitsRecordClaudeRouteNeedsNoServe(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AIMAN_SOCKET", filepath.Join(home, "no-such.sock"))
	in := `{"cwd":"/secret-project","rate_limits":{"five_hour":{"used_percentage":41}}}`
	if err := runLimits([]string{"record-claude"}, strings.NewReader(in), &strings.Builder{}); err != nil {
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

func TestServeConfigTurnsTheClaudeWrapperOn(t *testing.T) {
	home := t.TempDir()
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"statusLine":{"type":"command","command":"my-line","padding":2}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Limits.ClaudeStatusline = true
	cfg.Verify.BeforePR = true
	opts := agentHookOptions(cfg)
	if !opts.BeforePR || !opts.ClaudeStatusline {
		t.Fatalf("options %+v", opts)
	}
	if _, err := agenthook.EnsureOnHostWith(home, opts); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		StatusLine struct {
			Command string `json:"command"`
			Padding int    `json:"padding"`
		} `json:"statusLine"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.StatusLine.Command, "my-line") || !strings.Contains(got.StatusLine.Command, "aiman-claude-statusline-wrapper") || got.StatusLine.Padding != 2 {
		t.Fatalf("status line %+v", got.StatusLine)
	}
}
