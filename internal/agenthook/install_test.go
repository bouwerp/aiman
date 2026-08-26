package agenthook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureOnHostWritesReporterOnlyWhenNoAgents(t *testing.T) {
	home := t.TempDir()
	results, err := EnsureOnHost(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results=%d %+v", len(results), results)
	}
	if results[0].Action != ActionInstalled {
		t.Fatalf("action %s", results[0].Action)
	}
	body, err := os.ReadFile(reporterPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "report-agent-session --from-stdin") {
		t.Fatalf("script: %s", body)
	}
}

func TestEnsureOnHostMergesClaudeWithoutClobber(t *testing.T) {
	home := t.TempDir()
	claude := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claude, 0o700); err != nil {
		t.Fatal(err)
	}
	orig := []byte(`{"permissions":{"defaultMode":"auto"},"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"echo pre"}]}]}}`)
	if err := os.WriteFile(filepath.Join(claude, "settings.json"), orig, 0o600); err != nil {
		t.Fatal(err)
	}
	results, err := EnsureOnHost(home)
	if err != nil {
		t.Fatal(err)
	}
	if Summarize(results) == "" {
		t.Fatal("empty summary")
	}
	raw, err := os.ReadFile(filepath.Join(claude, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	if root["permissions"] == nil {
		t.Fatalf("clobbered: %s", raw)
	}
	hooks, _ := root["hooks"].(map[string]any)
	if hooks["PreToolUse"] == nil {
		t.Fatalf("lost PreToolUse: %s", raw)
	}
	if !strings.Contains(string(raw), Marker) {
		t.Fatalf("missing marker: %s", raw)
	}
	again, err := EnsureOnHost(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range again {
		if r.Path == filepath.Join(claude, "settings.json") && r.Action != ActionCurrent {
			t.Fatalf("second ensure action=%s", r.Action)
		}
	}
}

func TestEnsureOnHostSkipsMissingAgentDirs(t *testing.T) {
	home := t.TempDir()
	if _, err := EnsureOnHost(home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".cursor")); !os.IsNotExist(err) {
		t.Fatalf("cursor dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".grok")); !os.IsNotExist(err) {
		t.Fatalf("grok dir: %v", err)
	}
}

func TestEnsureOnHostWritesGrokOwnedFile(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".grok"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureOnHost(home); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".grok", "hooks", "aiman.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "SessionStart") || !strings.Contains(string(raw), "SessionEnd") {
		t.Fatalf("%s", raw)
	}
	if !strings.Contains(string(raw), "idle_prompt") || !strings.Contains(string(raw), Marker) {
		t.Fatalf("%s", raw)
	}
}

func TestEnsureOnHostWritesOpenCodePlugin(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".config", "opencode"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureOnHost(home); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".config", "opencode", "plugins", "aiman-native-session.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "report-agent-session") || !strings.Contains(string(raw), "session.idle") {
		t.Fatalf("%s", raw)
	}
}

func TestEnsureOnHostDoesNotTouchAgeni(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ageni"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureOnHost(home); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(home, ".ageni"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("ageni was touched: %v", entries)
	}
}

// The reporter must find the aiman binary without being handed a path.
//
// AIMAN_BIN_PATH used to be injected by whoever created the session — the
// laptop, for a remote session — so remote hooks were pointed at a path that
// did not exist there and every report silently failed. The script now
// resolves the binary where it actually runs, and still honours an explicit
// override when one is valid.
func TestReporterScriptResolvesBinaryWithoutInjectedPath(t *testing.T) {
	for _, want := range []string{
		`command -v aiman`,
		`$HOME/.local/bin/aiman`,
		`${AIMAN_BIN_PATH:-}`,
	} {
		if !strings.Contains(reporterScript, want) {
			t.Errorf("reporter script should contain %q so it works on the session's own host", want)
		}
	}
	// Still a no-op outside an aiman session, and still never fails the agent.
	if !strings.Contains(reporterScript, `[ "${AIMAN_ENV:-}" = 1 ] || exit 0`) {
		t.Error("reporter must stay inert outside an aiman session")
	}
}
