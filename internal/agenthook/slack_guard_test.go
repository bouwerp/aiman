package agenthook

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// gateGroups is a settings file's PreToolUse groups without the Slack and
// artifact guards, which every Claude install carries.
func gateGroups(hooks map[string]any) []any {
	groups, _ := hooks["PreToolUse"].([]any)
	var out []any
	for _, g := range groups {
		if !commandHasMarker(g, SlackGuardMarker) && !commandHasMarker(g, ArtifactGuardMarker) {
			out = append(out, g)
		}
	}
	return out
}

// TestSlackGuardInstalledForClaude adds one guard group on Claude's Slack MCP
// tools, keeps it on a second install, and leaves the gate groups alone.
func TestSlackGuardInstalledForClaude(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := EnsureOnHostWith(home, Options{CIGate: true}); err != nil {
			t.Fatal(err)
		}
	}
	groups, _ := readClaudeHooks(t, home)["PreToolUse"].([]any)
	var guards []map[string]any
	for _, g := range groups {
		if commandHasMarker(g, SlackGuardMarker) {
			guards = append(guards, g.(map[string]any))
		}
	}
	if len(guards) != 1 || guards[0]["matcher"] != slackGuardMatcher || len(gateGroups(readClaudeHooks(t, home))) != 1 {
		t.Fatalf("PreToolUse groups = %v", groups)
	}
	re := regexp.MustCompile("^(?:" + slackGuardMatcher + ")$")
	for _, tool := range []string{"mcp__plugin_slack_slack__slack_send_message", "mcp__claude_ai_Slack__slack_search_public"} {
		if !re.MatchString(tool) {
			t.Errorf("matcher misses %s", tool)
		}
	}
	for _, tool := range []string{"Bash", "mcp__claude_ai_Gmail__send_message"} {
		if re.MatchString(tool) {
			t.Errorf("matcher catches %s", tool)
		}
	}
}

// TestSlackGuardScriptRefusesOnlyInSessions exits 2 with the aiman slack
// pointer inside an Aiman session and 0 anywhere else.
func TestSlackGuardScriptRefusesOnlyInSessions(t *testing.T) {
	home := t.TempDir()
	if _, err := EnsureOnHost(home); err != nil {
		t.Fatal(err)
	}
	run := func(env ...string) (int, string) {
		cmd := exec.Command("sh", slackGuardPath(home))
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
		cmd.Stdin = strings.NewReader(`{"tool_name":"mcp__plugin_slack_slack__slack_send_message"}`)
		out, err := cmd.CombinedOutput()
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), string(out)
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0, string(out)
	}
	if code, out := run("AIMAN_ENV=1"); code != 2 || !strings.Contains(out, "use aiman slack") {
		t.Fatalf("in a session: exit %d %q", code, out)
	}
	if code, _ := run(); code != 0 {
		t.Fatalf("outside a session: exit %d", code)
	}
}
