package usecase

import (
	"bytes"
	"encoding/json"
	"fmt"
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
// release. Only Esc was pressed to produce the *_after_esc screens. The
// codex_splash, codex_hooks_review, codex_ready, codex_typed and
// codex_submitted screens are Codex 0.166.0 in a fresh repository with three
// untrusted hooks, ANSI stripped and the directory renamed. codex_model_gone is
// Codex 0.166.0 launched with --model gpt-5.4-mini after that id was retired.
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
		re, live, gone string
	}{
		{"Skip until next version", "codex_update", "codex_after_esc"},
		{"latest version has been released", "deepcode_update", "deepcode_after_esc"},
		{"Hooks need review", "codex_hooks_review", "codex_ready"},
		{"enter/esc continue", "codex_model_gone", "codex_ready"},
	}
	for _, c := range cases {
		var d infraAgent.UpdateDialog
		for _, x := range infraAgent.UpdateDialogs {
			if x.Re == c.re {
				d = x
			}
		}
		if d.Re == "" {
			t.Fatalf("no dialog %q", c.re)
		}
		live := bootFixture(t, c.live)
		gone := bootFixture(t, c.gone)
		n := infraAgent.UpdateDialogTailLines
		if !grep(d.Re, captureJSON(t, tailRows(live, n))) {
			t.Errorf("%s: grep -E %q misses the live dialog", c.live, d.Re)
		}
		if grep(d.Re, captureJSON(t, tailRows(gone, n))) {
			t.Errorf("%s: grep -E %q matches the screen after esc", c.gone, d.Re)
		}
		for _, other := range infraAgent.UpdateDialogs {
			if other.Re != d.Re && grep(other.Re, []byte(live)) {
				t.Errorf("pattern %q matches the %s dialog", other.Re, c.live)
			}
		}
		if got, ok := infraAgent.MatchUpdateDialog(live); !ok || got.Re != d.Re {
			t.Errorf("MatchUpdateDialog(%s) = %v, %v", c.live, got, ok)
		}
		if got, ok := infraAgent.MatchUpdateDialog(gone); ok {
			t.Errorf("MatchUpdateDialog(%s) = %v after esc", c.gone, got)
		}
	}
}

// Codex 0.166 asks to review new hooks before its input box comes up. Its
// rows start with "›", so a boot wait that reads it as ready types the first
// prompt into the menu. Esc continues without trusting anything.
func TestBootWaitSkipsCodexHookReviewWithEsc(t *testing.T) {
	f := newFakeBoot(t)
	f.screen["hooks"] = bootFixture(t, "codex_hooks_review")
	f.screen["ready"] = bootFixture(t, "codex_ready")
	f.next["hooks/esc"] = "ready"
	out, keys := f.run("hooks")
	if strings.Join(keys, ",") != "esc" {
		t.Fatalf("keys = %v, want exactly one esc", keys)
	}
	assertNoConfirmKey(t, keys)
	if got := bootOutcome(out); strings.HasPrefix(got, "timeout:") || got == "" {
		t.Fatalf("outcome = %q, want the ready composer after esc\n%s", got, out)
	}
}

// Codex draws "› Ask Codex to do anything" on its splash, before any trust
// or hook-review dialog and before the model footer. Taking the splash for a
// ready composer types the prompt into whichever dialog follows it.
func TestCodexSplashIsNotReady(t *testing.T) {
	f := newFakeBoot(t)
	f.screen["splash"] = bootFixture(t, "codex_splash") // never goes away
	out, keys := f.run("splash")
	if len(keys) != 0 {
		t.Fatalf("the splash got keys %v", keys)
	}
	if got := bootOutcome(out); got != "timeout:20" {
		t.Fatalf("outcome = %q, want timeout:20: the splash is not ready", got)
	}

	f = newFakeBoot(t)
	f.screen["ready"] = bootFixture(t, "codex_ready")
	out, _ = f.run("ready")
	if got := bootOutcome(out); got != "ready:0" {
		t.Fatalf("outcome = %q, want ready:0 for the composer with its footer", got)
	}
}

// Codex launched on a retired model blocks on a notice. A prompt typed there
// is lost: the text is ignored and its Return only dismisses the notice. The
// notice offers no choice, and esc continues on the replacement model.
func TestBootWaitSkipsCodexModelGoneWithEsc(t *testing.T) {
	f := newFakeBoot(t)
	f.screen["gone"] = bootFixture(t, "codex_model_gone")
	f.screen["ready"] = bootFixture(t, "codex_ready")
	f.next["gone/esc"] = "ready"
	out, keys := f.run("gone")
	if strings.Join(keys, ",") != "esc" {
		t.Fatalf("keys = %v, want exactly one esc", keys)
	}
	assertNoConfirmKey(t, keys)
	if got := bootOutcome(out); got != "update:1" {
		t.Fatalf("outcome = %q, want update:1", got)
	}
}

// The recorded order in an untrusted directory: trust, then the retired-model
// notice about four seconds later. The wait has to outlast the trust answer, or
// the prompt goes into the notice.
func TestBootWaitKeepsPollingAfterTrust(t *testing.T) {
	f := newFakeBoot(t)
	f.screen["trust"] = codexTrustScreen
	f.screen["gone"] = bootFixture(t, "codex_model_gone")
	f.screen["ready"] = bootFixture(t, "codex_ready")
	f.next["trust/enter"] = "gone"
	f.next["gone/esc"] = "ready"
	out, keys := f.run("trust")
	if strings.Join(keys, ",") != "enter,esc" {
		t.Fatalf("keys = %v, want enter then esc", keys)
	}
	if got := bootOutcome(out); got != "update:1" {
		t.Fatalf("outcome = %q, want update:1", got)
	}
}

// Trust text that stays on screen after the answer must not earn another Enter,
// and the post-trust wait is short.
func TestBootWaitAnswersTrustOnce(t *testing.T) {
	f := newFakeBoot(t)
	f.screen["stuck"] = "  Trust this folder?\n"
	out, keys := f.run("stuck")
	if strings.Join(keys, ",") != "enter" {
		t.Fatalf("keys = %v, want one enter", keys)
	}
	if got := bootOutcome(out); got != fmt.Sprintf("trust:%d", bootWaitAfterTrust) {
		t.Fatalf("outcome = %q, want trust:%d", got, bootWaitAfterTrust)
	}
}

// A style change inside a phrase must not hide it from the boot wait. Each
// screen has dim and colour changes inside the words a pattern looks for.
func TestBootWaitReadsThroughStyling(t *testing.T) {
	dim := func(s string) string { return "\x1b[2m" + s + "\x1b[22m" }
	cases := []struct {
		name, screen, wantKeys, wantOutcome string
	}{
		{"ready hint", "\n  " + dim("?") + " for " + "\x1b[38;5;8;49m" + "shortcuts\x1b[0m\n", "", "ready:0"},
		{"trust", "\n  Do " + dim("you") + " trust this?\n", "enter", "trust:" + fmt.Sprint(bootWaitAfterTrust)},
		{"retired model", "\n  " + dim("enter/esc") + " continue · ^c quit\n", "esc", "update:20"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeBoot(t)
			f.screen["s"] = c.screen
			out, keys := f.run("s")
			got := strings.Join(keys, ",")
			if c.wantKeys == "esc" {
				got = strings.Split(got, ",")[0] // the fake screen never clears
			}
			if got != c.wantKeys {
				t.Fatalf("keys = %v, want %q", keys, c.wantKeys)
			}
			if outcome := bootOutcome(out); outcome != c.wantOutcome {
				t.Fatalf("outcome = %q, want %q", outcome, c.wantOutcome)
			}
		})
	}
}

func TestMatchUpdateDialogReadsThroughStyling(t *testing.T) {
	styled := "\n  \x1b[2menter/esc\x1b[22m continue · ^c quit"
	if got, ok := infraAgent.MatchUpdateDialog(styled); !ok || got.Key != "esc" {
		t.Fatalf("MatchUpdateDialog = %+v, %v", got, ok)
	}
}
