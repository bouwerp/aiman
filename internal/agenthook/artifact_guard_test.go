package agenthook

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestArtifactGuardInstalledForClaude adds one guard group on Claude's
// Artifact tool and keeps it at one on a second install.
func TestArtifactGuardInstalledForClaude(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := EnsureOnHost(home); err != nil {
			t.Fatal(err)
		}
	}
	groups, _ := readClaudeHooks(t, home)["PreToolUse"].([]any)
	n := 0
	for _, g := range groups {
		if commandHasMarker(g, ArtifactGuardMarker) {
			n++
			if g.(map[string]any)["matcher"] != artifactGuardMatcher {
				t.Fatalf("matcher %v", g.(map[string]any)["matcher"])
			}
		}
	}
	if n != 1 {
		t.Fatalf("artifact guard groups = %d in %v", n, groups)
	}
	re := regexp.MustCompile("^(?:" + artifactGuardMatcher + ")$")
	if !re.MatchString("Artifact") || re.MatchString("ArtifactData") || re.MatchString("Bash") {
		t.Fatal("matcher must catch exactly Artifact")
	}
}

// TestArtifactGuardScript refuses a publish in a session, allows reads, and
// allows everything outside a session.
func TestArtifactGuardScript(t *testing.T) {
	home := t.TempDir()
	if _, err := EnsureOnHost(home); err != nil {
		t.Fatal(err)
	}
	run := func(input string, env ...string) (int, string) {
		cmd := exec.Command("sh", artifactGuardPath(home))
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.CombinedOutput()
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), string(out)
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0, string(out)
	}
	publish := `{"tool_name":"Artifact","tool_input":{"file_path":"x.html"}}`
	if code, out := run(publish, "AIMAN_ENV=1"); code != 2 || !strings.Contains(out, "aiman artifact publish") {
		t.Fatalf("publish in session: %d %q", code, out)
	}
	if code, _ := run(`{"tool_name":"Artifact","tool_input":{"action":"quickstart","intent":"other"}}`, "AIMAN_ENV=1"); code != 2 {
		t.Fatalf("quickstart in session: %d", code)
	}
	for _, a := range []string{"read", "list", "open"} {
		if code, out := run(`{"tool_name":"Artifact","tool_input":{"action":"`+a+`","url":"u"}}`, "AIMAN_ENV=1"); code != 0 {
			t.Fatalf("%s in session: %d %q", a, code, out)
		}
	}
	if code, _ := run(publish); code != 0 {
		t.Fatalf("outside a session: %d", code)
	}
}
