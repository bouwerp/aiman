package agenthook

// Acceptance tests for ci-minutes piece C (docs/specs/ci-minutes.md,
// Structure and Behaviour 2-3): the PreToolUse CI gate hook.

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func hookPayload(command string) []byte {
	b, _ := json.Marshal(map[string]any{
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": command, "description": "run it"},
	})
	return b
}

func TestDraftGateQuestionParsesCommandField(t *testing.T) {
	cases := []struct {
		command string
		ok      bool
		kind    string
		draft   bool
	}{
		{"gh pr create --base main --title x", true, "create", false},
		{"gh pr create --draft --base main", true, "create", true},
		{"gh pr create -d --fill", true, "create", true},
		{"gh -R o/r pr new --draft", true, "create", true},
		{"gh --repo o/r pr create", true, "create", false},
		{"gh pr ready", true, "ready", false},
		{"gh pr ready 12", true, "ready", false},
		{"gh pr ready --undo", false, "", false},
		{"git push", true, "push", false},
		{"git push -u origin HEAD", true, "push", false},
		{"git commit -m x && git push", true, "push", false},
		{"git status", false, "", false},
		{"gh pr view --json isDraft", false, "", false},
		{"echo 'gh pr create'", false, "", false},
	}
	for _, c := range cases {
		q, ok := GateQuestionFor(hookPayload(c.command))
		if ok != c.ok {
			t.Fatalf("%q: gated=%v, want %v", c.command, ok, c.ok)
		}
		if !ok {
			continue
		}
		if q.Kind != c.kind || q.Draft != c.draft || q.Command != c.command {
			t.Fatalf("%q: %+v, want kind %s draft %v and the command verbatim", c.command, q, c.kind, c.draft)
		}
	}
}

func TestDraftGateQuestionIgnoresOtherFields(t *testing.T) {
	b, _ := json.Marshal(map[string]any{
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "ls", "description": "before gh pr create and git push"},
	})
	if q, ok := GateQuestionFor(b); ok {
		t.Fatalf("description text gated the call: %+v", q)
	}
	if _, ok := GateQuestionFor([]byte("gh pr create")); ok {
		t.Fatal("a payload that is not JSON was gated")
	}
}

// runCIGateScript runs the installed script with a stub aiman that records
// its argv and stdin, and a stub gh that must never run.
func runCIGateScript(t *testing.T, env []string, stubExit int, payload []byte) (code int, args, stdin string, ghRan bool) {
	t.Helper()
	home := t.TempDir()
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(home, "args")
	stdinFile := filepath.Join(home, "stdin")
	ghFile := filepath.Join(home, "gh-ran")
	aiman := "#!/bin/sh\necho \"$@\" > " + argsFile + "\ncat > " + stdinFile + "\necho refused-by-stub >&2\nexit " + itoa(stubExit) + "\n"
	if err := os.WriteFile(filepath.Join(binDir, "aiman"), []byte(aiman), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte("#!/bin/sh\ntouch "+ghFile+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(home, CIGateMarker)
	if err := os.WriteFile(script, []byte(ciGateScript), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", script)
	cmd.Env = append([]string{"HOME=" + home, "PATH=" + binDir + ":/usr/bin:/bin", "AIMAN_BIN_PATH=" + filepath.Join(binDir, "aiman")}, env...)
	cmd.Stdin = strings.NewReader(string(payload))
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		code = exit.ExitCode()
	default:
		t.Fatal(err)
	}
	a, _ := os.ReadFile(argsFile)
	s, _ := os.ReadFile(stdinFile)
	_, statErr := os.Stat(ghFile)
	return code, strings.TrimSpace(string(a)), string(s), statErr == nil
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

var sessionEnv = []string{"AIMAN_ENV=1", "AIMAN_ID=s1"}

func TestDraftGateScriptHandsPayloadToAiman(t *testing.T) {
	payload := hookPayload("gh pr create --draft")
	code, args, stdin, ghRan := runCIGateScript(t, sessionEnv, 0, payload)
	if code != 0 {
		t.Fatalf("allowed gate exited %d", code)
	}
	if args != "ci gate --hook" {
		t.Fatalf("aiman args %q, want %q", args, "ci gate --hook")
	}
	if stdin != string(payload) {
		t.Fatalf("aiman stdin %q, want the hook payload", stdin)
	}
	if ghRan {
		t.Fatal("the hook script ran gh")
	}
}

func TestDraftGateScriptBlocksWithExit2(t *testing.T) {
	for _, stubExit := range []int{2, 1} {
		code, _, _, _ := runCIGateScript(t, sessionEnv, stubExit, hookPayload("git push"))
		if code != 2 {
			t.Fatalf("aiman exit %d: script exit %d, want 2 (fail closed)", stubExit, code)
		}
	}
}

func TestDraftGateScriptSkipsUnrelatedAndOutsideSessions(t *testing.T) {
	code, args, _, _ := runCIGateScript(t, sessionEnv, 2, hookPayload("go test ./..."))
	if code != 0 || args != "" {
		t.Fatalf("unrelated command: exit %d, aiman args %q; want 0 and no call", code, args)
	}
	code, args, _, _ = runCIGateScript(t, nil, 2, hookPayload("git push"))
	if code != 0 || args != "" {
		t.Fatalf("outside a session: exit %d, aiman args %q; want 0 and no call", code, args)
	}
}

func TestDraftGateScriptFailsClosedWithoutAiman(t *testing.T) {
	home := t.TempDir()
	script := filepath.Join(home, CIGateMarker)
	if err := os.WriteFile(script, []byte(ciGateScript), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", script)
	cmd.Env = append([]string{"HOME=" + home, "PATH=/usr/bin:/bin", "AIMAN_BIN_PATH=/nonexistent"}, sessionEnv...)
	cmd.Stdin = strings.NewReader(string(hookPayload("gh pr ready")))
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Fatalf("missing aiman: err %v, want exit 2", err)
	}
}

func claudeHookCommands(t *testing.T, home string) []string {
	t.Helper()
	groups, _ := readClaudeHooks(t, home)["PreToolUse"].([]any)
	var out []string
	for _, g := range groups {
		out = append(out, toString(g))
	}
	return out
}

func TestDraftGateInstallIndependentOfBeforePR(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureOnHostWith(home, Options{CIGate: true}); err != nil {
		t.Fatal(err)
	}
	groups := claudeHookCommands(t, home)
	if len(groups) != 1 || !strings.Contains(groups[0], CIGateMarker) || !strings.Contains(groups[0], `"matcher":"Bash"`) {
		t.Fatalf("CIGate alone: PreToolUse %v", groups)
	}
	if _, err := os.Stat(filepath.Join(home, ".aiman", "hooks", CIGateMarker)); err != nil {
		t.Fatalf("ci gate script not written: %v", err)
	}

	if _, err := EnsureOnHostWith(home, Options{CIGate: true, BeforePR: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureOnHostWith(home, Options{CIGate: true, BeforePR: true}); err != nil {
		t.Fatal(err)
	}
	groups = claudeHookCommands(t, home)
	joined := strings.Join(groups, "\n")
	if len(groups) != 2 || !strings.Contains(joined, CIGateMarker) || !strings.Contains(joined, GateMarker) {
		t.Fatalf("both on (twice): PreToolUse %v, want one group each", groups)
	}

	if _, err := EnsureOnHostWith(home, Options{BeforePR: true}); err != nil {
		t.Fatal(err)
	}
	groups = claudeHookCommands(t, home)
	if len(groups) != 1 || strings.Contains(groups[0], CIGateMarker) || !strings.Contains(groups[0], GateMarker) {
		t.Fatalf("CIGate off: PreToolUse %v, want only the verify gate", groups)
	}
	if _, err := os.Stat(filepath.Join(home, ".aiman", "hooks", CIGateMarker)); !os.IsNotExist(err) {
		t.Fatalf("ci gate script survived CIGate off: %v", err)
	}
}
