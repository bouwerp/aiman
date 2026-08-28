package usecase

import (
	"context"
	"strings"
	"testing"
	"time"
)

// paneRemote answers pty capture with a scripted sequence of panes, so a test
// can say "the composer still holds the prompt, then it clears".
type paneRemote struct {
	cmds   []string
	panes  []string
	paneAt int
}

func (r *paneRemote) WriteFile(_ context.Context, _ string, _ []byte) error { return nil }

func (r *paneRemote) Execute(_ context.Context, cmd string) (string, error) {
	r.cmds = append(r.cmds, cmd)
	if !strings.Contains(cmd, "pty capture") {
		return "", nil
	}
	text := ""
	if r.paneAt < len(r.panes) {
		text = r.panes[r.paneAt]
		r.paneAt++
	} else if len(r.panes) > 0 {
		text = r.panes[len(r.panes)-1]
	}
	return `{"type":"pane_read","text":` + quoteJSON(text) + `}`, nil
}

func quoteJSON(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

func (r *paneRemote) enters() int {
	n := 0
	for _, c := range r.cmds {
		n += strings.Count(c, "--key enter")
	}
	return n
}

func TestMain(m *testing.M) {
	submitSettle = time.Millisecond
	m.Run()
}

// Agents drop input while mid-render or still booting, so a prompt can sit in
// the composer fully typed and never submitted — which looks from the outside
// like the agent ignoring its instructions.
func TestSendPTYFileConfirmedRetriesAStuckComposer(t *testing.T) {
	prompt := "Review PR 439 and report what you find"
	r := &paneRemote{panes: []string{
		"› " + prompt,        // still sitting in the composer
		"› " + prompt,        // still there after the second Return
		"• Working (2s)\n› ", // finally submitted
	}}
	if err := SendPTYFileConfirmed(context.Background(), r, "sess-1", "/tmp/p", prompt); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got := r.enters(); got < 2 {
		t.Errorf("a stuck composer should get another Return, got %d", got)
	}
}

// A prompt that submitted first time must not get a second Return: the composer
// is empty by then and an extra Return can send a blank turn.
func TestSendPTYFileConfirmedStopsOnceTheComposerClears(t *testing.T) {
	prompt := "Review PR 439 and report what you find"
	r := &paneRemote{panes: []string{"• Working (2s)\n› Ask Codex to do anything"}}
	if err := SendPTYFileConfirmed(context.Background(), r, "sess-1", "/tmp/p", prompt); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got := r.enters(); got != 1 {
		t.Errorf("expected exactly one Return, got %d", got)
	}
}

// Retrying for ever would hammer a session whose pane simply looks like this.
func TestSendPTYFileConfirmedGivesUp(t *testing.T) {
	prompt := "Review PR 439 and report what you find"
	r := &paneRemote{panes: []string{"› " + prompt}}
	if err := SendPTYFileConfirmed(context.Background(), r, "sess-1", "/tmp/p", prompt); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got := r.enters(); got != submitAttempts {
		t.Errorf("got %d Returns, want the %d-attempt cap", got, submitAttempts)
	}
}

// A pane that cannot be read is not a reason to fail a delivery that may well
// have worked.
func TestSendPTYFileConfirmedToleratesAnUnreadablePane(t *testing.T) {
	r := &recordingRemote{err: nil, out: "not json at all"}
	if err := SendPTYFileConfirmed(context.Background(), r, "sess-1", "/tmp/p", "some prompt text here"); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// The marker is the tail, because agents wrap and scroll a long prompt: the
// beginning goes off screen while the end sits on the composer line.
func TestSubmitMarkerUsesTheTail(t *testing.T) {
	long := "please review the pull request and then summarise the findings for me"
	got := submitMarker(long)
	if !strings.HasSuffix(long, got) {
		t.Errorf("marker %q is not a tail of the prompt", got)
	}
	if len([]rune(got)) > 24 {
		t.Errorf("marker too long: %q", got)
	}
	if submitMarker("   ") != "" {
		t.Error("an empty prompt must disable the check")
	}
	if got := submitMarker("hi"); got != "hi" {
		t.Errorf("a short prompt should be matched whole, got %q", got)
	}
}

// Newlines never survive into the composer as-is, so a multi-line prompt has to
// be flattened before it is looked for.
func TestSubmitMarkerFlattensNewlines(t *testing.T) {
	if got := submitMarker("first line\nsecond line"); strings.Contains(got, "\n") {
		t.Errorf("marker should be flat, got %q", got)
	}
}
