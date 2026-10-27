package agenthook

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
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

func TestGuardsPRCreate(t *testing.T) {
	for _, name := range []string{"Claude Code", "claude"} {
		if !GuardsPRCreate(name) {
			t.Fatalf("%q should support PreToolUse", name)
		}
	}
	for _, name := range []string{"codex", "grok", "kilo"} {
		if GuardsPRCreate(name) {
			t.Fatalf("%q should be unprotected", name)
		}
	}
}

func toString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestVerifyGateScriptMatcherAndTimeout(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "aiman")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(home, "gate.sh")
	if err := os.WriteFile(script, []byte(verifyGateScript), 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(input string) int {
		t.Helper()
		cmd := exec.Command("/bin/sh", script)
		cmd.Env = []string{"AIMAN_ENV=1", "AIMAN_ID=x", "AIMAN_BIN_PATH=" + bin, "HOME=" + home, "PATH=/usr/bin:/bin"}
		cmd.Stdin = strings.NewReader(input)
		err := cmd.Run()
		if err == nil {
			return 0
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		t.Fatal(err)
		return 0
	}
	for _, input := range []string{`{"command":"gh pr create"}`, `{"command":"gh pr new"}`, `{"command":"gh -R o/r pr create"}`} {
		if code := run(input); code != 2 {
			t.Fatalf("%s: exit %d", input, code)
		}
	}
	if code := run(`{"command":"git status"}`); code != 0 {
		t.Fatalf("unrelated command blocked: %d", code)
	}
}

func TestVerifyGateScriptFailsClosedWithoutAiman(t *testing.T) {
	home := t.TempDir()
	script := filepath.Join(home, "gate.sh")
	if err := os.WriteFile(script, []byte(verifyGateScript), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", script)
	cmd.Env = []string{"AIMAN_ENV=1", "AIMAN_ID=x", "HOME=" + home, "AIMAN_BIN_PATH=/nonexistent", "PATH=/usr/bin:/bin"}
	cmd.Stdin = strings.NewReader(`{"command":"gh pr create"}`)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Fatalf("err = %v output=%q", err, out)
	}
}

func TestBeforePROffRemovesGate(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureOnHost(home, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(gatePath(home)); err != nil {
		t.Fatalf("gate script missing after install: %v", err)
	}
	if _, err := EnsureOnHost(home, false); err != nil {
		t.Fatal(err)
	}
	if hooks := readClaudeHooks(t, home); hooks["PreToolUse"] != nil {
		t.Fatalf("gate hook survived before_pr off: %v", hooks["PreToolUse"])
	}
	if _, err := os.Stat(gatePath(home)); !os.IsNotExist(err) {
		t.Fatalf("gate script survived before_pr off: %v", err)
	}
}
