package usecase

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	infraAgent "github.com/bouwerp/aiman/internal/infra/agent"
)

// The fixtures under testdata/bootdialogs are real screens: Codex 0.164.0 with
// a throwaway CODEX_HOME whose version.json named a newer release, and Deep
// Code 0.4.3 with a throwaway HOME whose update-check.json held a pending
// release. Only Esc was pressed to produce the *_after_esc screens.
func bootFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "bootdialogs", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// captureJSON is what `aiman pty capture` prints: indented JSON with the
// screen in "text", HTML characters escaped.
func captureJSON(t *testing.T, screen string) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{"hash": "h", "text": screen, "type": "pane_read"}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeBoot runs the PTY boot-wait script against a fake `aiman` whose screen
// is a state machine: each state is a screen, and a key press may move to the
// next state. sleep is a no-op so the full budget runs instantly.
type fakeBoot struct {
	t      *testing.T
	dir    string
	screen map[string]string
	next   map[string]string // "state/key" -> state
}

func newFakeBoot(t *testing.T) *fakeBoot {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	return &fakeBoot{t: t, dir: t.TempDir(), screen: map[string]string{}, next: map[string]string{}}
}

const fakeAimanScript = `#!/bin/sh
d=$(dirname "$0")
state=$(cat "$d/state")
case "$1 $2" in
"pty capture")
  lines=0
  [ "$4" = "--lines" ] && lines=$5
  if [ "$lines" = 0 ]; then cat "$d/$state.full.json"; else cat "$d/$state.tail.json"; fi ;;
"pty input")
  key=$5
  echo "$key" >> "$d/keys"
  if [ -f "$d/next.$state.$key" ]; then cp "$d/next.$state.$key" "$d/state"; fi
  echo '{"type":"pty_input"}' ;;
*) exit 2 ;;
esac
`

func (f *fakeBoot) run(start string) (out string, keys []string) {
	t := f.t
	t.Helper()
	write := func(name string, data []byte, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(f.dir, name), data, mode); err != nil {
			t.Fatal(err)
		}
	}
	for state, screen := range f.screen {
		write(state+".full.json", captureJSON(t, screen), 0o644)
		write(state+".tail.json", captureJSON(t, tailRows(screen, infraAgent.UpdateDialogTailLines)), 0o644)
	}
	for k, v := range f.next {
		state, key, _ := strings.Cut(k, "/")
		write("next."+state+"."+key, []byte(v), 0o644)
	}
	write("state", []byte(start), 0o644)
	write("aiman", []byte(fakeAimanScript), 0o755)
	write("sleep", []byte("#!/bin/sh\nexit 0\n"), 0o755)

	cmd := exec.Command("bash", "-c", acceptWorkspaceTrustPTY("sess-1"))
	cmd.Env = append(os.Environ(), "PATH="+f.dir+":/usr/bin:/bin")
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("boot wait: %v\n%s", err, raw)
	}
	if b, err := os.ReadFile(filepath.Join(f.dir, "keys")); err == nil {
		keys = strings.Fields(string(b))
	}
	return string(raw), keys
}

// tailRows mirrors the server's capture --lines N: trailing blank rows are
// already gone from a rendered screen, then the last N rows are kept.
func tailRows(screen string, n int) string {
	rows := strings.Split(strings.TrimRight(screen, "\n"), "\n")
	if len(rows) > n {
		rows = rows[len(rows)-n:]
	}
	return strings.Join(rows, "\n")
}

func bootOutcome(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "AIMAN_BOOTWAIT:"); ok {
			return rest
		}
	}
	return ""
}

func assertNoConfirmKey(t *testing.T, keys []string) {
	t.Helper()
	for _, k := range keys {
		if k == "enter" || strings.ContainsAny(k, "0123456789") {
			t.Fatalf("an update dialog got %q, which installs: keys %v", k, keys)
		}
	}
}

func TestBootWaitDismissesCodexUpdateWithEsc(t *testing.T) {
	f := newFakeBoot(t)
	f.screen["modal"] = bootFixture(t, "codex_update")
	f.screen["ready"] = bootFixture(t, "codex_after_esc")
	f.next["modal/esc"] = "ready"
	out, keys := f.run("modal")
	if strings.Join(keys, ",") != "esc" {
		t.Fatalf("keys = %v, want exactly one esc", keys)
	}
	if got := bootOutcome(out); got != "update:1" {
		t.Fatalf("outcome = %q, want update:1\n%s", got, out)
	}
}

// Deep Code leaves the dismissed prompt on the primary screen above its
// composer. Only the bottom rows count, so it is skipped once, not on every
// poll.
func TestBootWaitDismissesDeepcodeUpdateWithEsc(t *testing.T) {
	f := newFakeBoot(t)
	f.screen["prompt"] = bootFixture(t, "deepcode_update")
	f.screen["app"] = bootFixture(t, "deepcode_after_esc")
	f.next["prompt/esc"] = "app"
	out, keys := f.run("prompt")
	if strings.Join(keys, ",") != "esc" {
		t.Fatalf("keys = %v, want exactly one esc", keys)
	}
	if got := bootOutcome(out); !strings.HasPrefix(got, "update:") {
		t.Fatalf("outcome = %q, want update\n%s", got, out)
	}
}

// Codex's update menu rows start with "›", the same glyph as its composer.
// A boot wait that reads the modal as ready types the first prompt into it.
func TestCodexUpdateModalIsNotReady(t *testing.T) {
	f := newFakeBoot(t)
	f.screen["modal"] = bootFixture(t, "codex_update") // never goes away
	out, keys := f.run("modal")
	got := bootOutcome(out)
	if strings.HasPrefix(got, "ready:") {
		t.Fatalf("the update modal was taken for a ready composer: %q", got)
	}
	if len(keys) == 0 || keys[0] != "esc" {
		t.Fatalf("keys = %v, want esc first", keys)
	}
	assertNoConfirmKey(t, keys)
	if got != "update:20" {
		t.Fatalf("outcome = %q, want update:20 once the budget is spent", got)
	}
}

const codexTrustScreen = `> You are in /home/u/work

  Do you trust the contents of this directory? Working with untrusted contents
  comes with higher risk of prompt injection.

› 1. Yes, continue
  2. No, quit

  Press enter to continue`

func TestBootWaitChecksUpdateBeforeTrust(t *testing.T) {
	script := acceptWorkspaceTrustPTY("sess-1")
	upd := strings.Index(script, infraAgent.UpdateDialogs[0].Re)
	trust := strings.Index(script, "--key enter")
	ready := strings.Index(script, "reason=ready")
	if upd < 0 || trust < 0 || ready < 0 || upd > trust || trust > ready {
		t.Fatalf("want update, then trust, then ready in the loop: %s", script)
	}

	f := newFakeBoot(t)
	f.screen["modal"] = bootFixture(t, "codex_update")
	f.screen["trust"] = codexTrustScreen
	f.screen["ready"] = bootFixture(t, "codex_after_esc")
	f.next["modal/esc"] = "trust"
	f.next["trust/enter"] = "ready"
	out, keys := f.run("modal")
	if strings.Join(keys, ",") != "esc,enter" {
		t.Fatalf("keys = %v, want esc then enter", keys)
	}
	if got := bootOutcome(out); got != "update:1" {
		t.Fatalf("outcome = %q, want update:1", got)
	}
}

func TestBootWaitReadyWithoutDialog(t *testing.T) {
	f := newFakeBoot(t)
	f.screen["ready"] = bootFixture(t, "codex_after_esc")
	out, keys := f.run("ready")
	if len(keys) != 0 {
		t.Fatalf("a ready composer got keys %v", keys)
	}
	if got := bootOutcome(out); got != "ready:0" {
		t.Fatalf("outcome = %q, want ready:0", got)
	}
}

// The loop greps the JSON `aiman pty capture` prints, with grep -E. Each
// pattern must match its live dialog there, and must not match the screen
// after the dialog is gone: Codex keeps an "Update available!" banner above
// its composer, and Deep Code keeps the dismissed prompt above its app.
func TestUpdateDialogPatternsWithGrep(t *testing.T) {
	if _, err := exec.LookPath("grep"); err != nil {
		t.Skip("grep not available")
	}
	grep := func(re string, data []byte) bool {
		cmd := exec.Command("grep", "-qE", re)
		cmd.Stdin = bytes.NewReader(data)
		return cmd.Run() == nil
	}
	cases := []struct {
		agent, live, gone string
	}{
		{"codex", "codex_update", "codex_after_esc"},
		{"deepcode", "deepcode_update", "deepcode_after_esc"},
	}
	for _, c := range cases {
		var d infraAgent.UpdateDialog
		for _, x := range infraAgent.UpdateDialogs {
			if x.Agent == c.agent {
				d = x
			}
		}
		if d.Re == "" {
			t.Fatalf("no dialog for %s", c.agent)
		}
		live := bootFixture(t, c.live)
		gone := bootFixture(t, c.gone)
		n := infraAgent.UpdateDialogTailLines
		if !grep(d.Re, captureJSON(t, tailRows(live, n))) {
			t.Errorf("%s: grep -E %q misses the live dialog", c.agent, d.Re)
		}
		if grep(d.Re, captureJSON(t, tailRows(gone, n))) {
			t.Errorf("%s: grep -E %q matches the screen after esc", c.agent, d.Re)
		}
		for _, other := range infraAgent.UpdateDialogs {
			if other.Agent != c.agent && grep(other.Re, []byte(live)) {
				t.Errorf("%s pattern matches the %s dialog", other.Agent, c.agent)
			}
		}
		if got, ok := infraAgent.MatchUpdateDialog(live); !ok || got.Agent != c.agent {
			t.Errorf("MatchUpdateDialog(%s) = %v, %v", c.live, got, ok)
		}
		if got, ok := infraAgent.MatchUpdateDialog(gone); ok {
			t.Errorf("MatchUpdateDialog(%s) = %v after esc", c.gone, got)
		}
	}
}
