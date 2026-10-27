package agenthook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readClaudeHooks(t *testing.T, home string) map[string]any {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatal(err)
	}
	hooks, _ := root["hooks"].(map[string]any)
	return hooks
}

func TestPreToolUseInstalledForClaude(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureOnHost(home, true); err != nil {
		t.Fatal(err)
	}
	hooks := readClaudeHooks(t, home)
	groups, _ := hooks["PreToolUse"].([]any)
	if len(groups) != 1 {
		t.Fatalf("PreToolUse groups = %v", groups)
	}
	group, _ := groups[0].(map[string]any)
	if group["matcher"] != "Bash" || !strings.Contains(toString(group["hooks"]), GateMarker) {
		t.Fatalf("group = %+v", group)
	}
	if _, err := EnsureOnHost(home, true); err != nil {
		t.Fatal(err)
	}
	if got := len(readClaudeHooks(t, home)["PreToolUse"].([]any)); got != 1 {
		t.Fatalf("duplicate PreToolUse groups = %d", got)
	}
}

func TestPreToolUseNotInstalledWithoutBeforePR(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureOnHost(home); err != nil {
		t.Fatal(err)
	}
	if hooks := readClaudeHooks(t, home); hooks["PreToolUse"] != nil {
		t.Fatalf("PreToolUse installed without before_pr: %v", hooks["PreToolUse"])
	}
}

func TestSupportsPreToolUse(t *testing.T) {
	for _, name := range []string{"Claude Code", "claude"} {
		if !SupportsPreToolUse(name) {
			t.Fatalf("%q should support PreToolUse", name)
		}
	}
	for _, name := range []string{"codex", "grok", "kilo"} {
		if SupportsPreToolUse(name) {
			t.Fatalf("%q should be unprotected", name)
		}
	}
}

func toString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
