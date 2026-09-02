package usecase

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// literalEscape catches the bug this file exists for: a control character
// spelled as an escape sequence inside a shell command. Neither "\r" in double
// quotes nor '\x03' in single quotes is interpreted by a shell, and nothing
// unescapes them on the way through, so the agent receives the characters of the
// spelling — the prompt lands in the input box and is never submitted.
var literalEscape = regexp.MustCompile(`--data\s+["']?\\+[rnx]`)

func assertSubmits(t *testing.T, what, cmd string) {
	t.Helper()
	if literalEscape.MatchString(cmd) {
		t.Errorf("%s smuggles a control character through the shell as an escape: %s", what, cmd)
	}
	if !strings.Contains(cmd, "--key enter") {
		t.Errorf("%s never presses Return, so the prompt sits in the input box: %s", what, cmd)
	}
	// Return must be a separate write after a pause: an agent TUI that reads the
	// text and the Return together treats it as a paste and inserts a newline.
	// The submitting Return is the last one — an earlier one may belong to the
	// workspace-trust dialog the script clears first.
	keyAt := strings.LastIndex(cmd, "--key enter")
	sleepAt := strings.LastIndex(cmd[:keyAt], "sleep ")
	if sleepAt < 0 {
		t.Errorf("%s should pause before pressing Return: %s", what, cmd)
	}
}

func TestDeliverInitialPromptPTYSubmitsThePrompt(t *testing.T) {
	r := &recordingRemote{}
	DeliverInitialPromptPTY(context.Background(), r, "sess-1", "do the thing")
	if len(r.cmds) == 0 {
		t.Fatal("nothing was sent")
	}
	assertSubmits(t, "DeliverInitialPromptPTY", r.joined())
}

// The initial pause is dynamic (formatSleepSeconds(initialSettle)), not a
// hardcoded "sleep 1" — this pins the actual value so a future change to
// initialSettle is a deliberate edit here, not a silent drift.
func TestSendPTYFileConfirmedUsesInitialSettleForThePause(t *testing.T) {
	r := &recordingRemote{}
	if err := SendPTYFile(context.Background(), r, "sess-1", "/tmp/p"); err != nil {
		t.Fatal(err)
	}
	want := "sleep " + formatSleepSeconds(initialSettle)
	if !strings.Contains(r.joined(), want) {
		t.Errorf("expected %q before Return, got: %s", want, r.joined())
	}
}

func TestFormatSleepSeconds(t *testing.T) {
	cases := map[time.Duration]string{
		2 * time.Second:         "2",
		1500 * time.Millisecond: "1.5",
		0:                       "0",
	}
	for d, want := range cases {
		if got := formatSleepSeconds(d); got != want {
			t.Errorf("formatSleepSeconds(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestSendPTYFileSubmitsThePrompt(t *testing.T) {
	r := &recordingRemote{}
	if err := SendPTYFile(context.Background(), r, "sess-1", "/tmp/p"); err != nil {
		t.Fatal(err)
	}
	assertSubmits(t, "SendPTYFile", r.joined())
}

// SendSessionPrompt is what the agent API, peer messaging and scheduled prompts
// all go through, so a prompt that is typed but never submitted breaks all of
// them at once.
func TestSendSessionPromptSubmitsForPTYSessions(t *testing.T) {
	r := &recordingRemote{}
	s := domain.Session{ID: "sess-1", Backend: domain.BackendPTY}
	if err := SendSessionPrompt(context.Background(), r, s, "hello"); err != nil {
		t.Fatal(err)
	}
	assertSubmits(t, "SendSessionPrompt(pty)", r.joined())
}

// The interrupt has the same problem: '\x03' as text types four characters
// instead of interrupting the agent.
func TestRestartHandoffInterruptUsesARealControlCharacter(t *testing.T) {
	// A non-empty pane is needed to get past the "nothing live to hand off"
	// short-circuit. The summary file never appears, so the capture waits for it
	// — hence the deadline, which is how the real callers bound this too.
	r := &recordingRemote{out: "some agent output"}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = CaptureRestartSessionSummaryPTY(ctx, r, "sess-1", "/tmp/s")

	cmd := r.joined()
	if literalEscape.MatchString(cmd) {
		t.Errorf("interrupt/prompt smuggled an escape through the shell: %s", cmd)
	}
	if strings.Contains(cmd, `\x03`) {
		t.Errorf("ctrl-c must be the control byte, not its spelling: %s", cmd)
	}
}
