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

// After submit, Codex/Grok echo the prompt into the transcript above an empty
// composer. Matching the whole pane would look "stuck" forever and keep
// pressing Return into the next turn.
func TestComposerStillHoldsLooksOnlyAtTheBottom(t *testing.T) {
	prompt := "Review PR 439 and report what you find"
	marker := submitMarker(prompt)
	submitted := "user\n" + prompt + "\n• Working (2s)\n› "
	if composerStillHolds(submitted, marker) {
		t.Fatal("an echoed prompt above an empty composer must not count as stuck")
	}
	stuck := "• Ready\n› " + prompt
	if !composerStillHolds(stuck, marker) {
		t.Fatal("a prompt sitting on the composer line must count as stuck")
	}
}

// Child-session create used to detach a one-shot Enter. Codex often drops that
// Return while still booting, leaving the task typed and never submitted.
func TestDeliverInitialPromptPTYRetriesAStuckComposer(t *testing.T) {
	prompt := "Review PR 439 and report what you find"
	r := &paneRemote{panes: []string{
		"› " + prompt,
		"• Working (2s)\n› ",
	}}
	DeliverInitialPromptPTY(context.Background(), r, "sess-1", prompt)
	if got := r.enters(); got < 2 {
		t.Fatalf("initial prompt must retry a stuck composer, got %d Returns in %v", got, r.cmds)
	}
	for _, c := range r.cmds {
		if strings.Contains(c, "nohup") {
			t.Fatalf("initial prompt delivery must not detach; create would return before submit: %s", c)
		}
	}
}

// tmuxPaneRemote is paneRemote's tmux twin: it answers CaptureTmuxPane with a
// scripted sequence of panes instead of a JSON-wrapped `pty capture` reply.
type tmuxPaneRemote struct {
	cmds   []string
	panes  []string
	paneAt int
}

func (r *tmuxPaneRemote) WriteFile(_ context.Context, _ string, _ []byte) error { return nil }

func (r *tmuxPaneRemote) Execute(_ context.Context, cmd string) (string, error) {
	r.cmds = append(r.cmds, cmd)
	return "", nil
}

func (r *tmuxPaneRemote) CaptureTmuxPane(_ context.Context, _ string) (string, error) {
	text := ""
	if r.paneAt < len(r.panes) {
		text = r.panes[r.paneAt]
		r.paneAt++
	} else if len(r.panes) > 0 {
		text = r.panes[len(r.panes)-1]
	}
	return text, nil
}

func (r *tmuxPaneRemote) enters() int {
	n := 0
	for _, c := range r.cmds {
		n += strings.Count(c, "Enter")
	}
	return n
}

// The tmux twin of TestDeliverInitialPromptPTYRetriesAStuckComposer: the
// tmux delivery path never got the confirm/retry fix that landed for PTY, so
// a child session on the tmux backend could still leave the task typed and
// unsubmitted after a dropped Return.
func TestDeliverInitialPromptRetriesAStuckTmuxComposer(t *testing.T) {
	prompt := "Review PR 439 and report what you find"
	r := &tmuxPaneRemote{panes: []string{
		"› " + prompt,
		"• Working (2s)\n› ",
	}}
	DeliverInitialPrompt(context.Background(), r, "my-session", "sess-1", prompt, false)
	if got := r.enters(); got < 2 {
		t.Fatalf("initial prompt must retry a stuck tmux composer, got %d Enters in %v", got, r.cmds)
	}
	for _, c := range r.cmds {
		if strings.Contains(c, "nohup") {
			t.Fatalf("initial prompt delivery must not detach; create would return before submit: %s", c)
		}
	}
}

// A tmux prompt that submits first time must not get a second Enter.
func TestDeliverInitialPromptStopsOnceTmuxComposerClears(t *testing.T) {
	prompt := "Review PR 439 and report what you find"
	r := &tmuxPaneRemote{panes: []string{"• Working (2s)\n› Ask Codex to do anything"}}
	DeliverInitialPrompt(context.Background(), r, "my-session", "sess-1", prompt, false)
	if got := r.enters(); got != 1 {
		t.Fatalf("expected exactly one Enter, got %d in %v", got, r.cmds)
	}
}

// Serve confirms its own deliveries against a locally captured screen, with
// the same tail-of-prompt check the laptop-side confirm loop uses.
func TestPromptStillInComposer(t *testing.T) {
	prompt := "Your to-do list was changed\nAdjust your work to match"
	if !PromptStillInComposer("• Ready\n› Your to-do list was changed Adjust your work to match", prompt) {
		t.Fatal("a prompt on the composer line must count as held")
	}
	if PromptStillInComposer("Adjust your work to match\n• Working (1s)\n› ", prompt) {
		t.Fatal("an echoed prompt above an empty composer must not count as held")
	}
	if PromptStillInComposer("› anything", "  ") {
		t.Fatal("an empty prompt disables the check")
	}
}

// Serve types into its own PTY with the same budget the remote path learned it
// needs; the defaults are that path's values, not a second set to drift.
func TestDefaultSubmitTimingMatchesConfirmedPath(t *testing.T) {
	got := DefaultSubmitTiming()
	if got.Initial != initialSettle || got.Settle != submitSettle || got.Attempts != submitAttempts {
		t.Fatalf("DefaultSubmitTiming = %+v, want initial %s settle %s attempts %d", got, initialSettle, submitSettle, submitAttempts)
	}
}

// TypeAndSubmit reports when Return was first pressed, so a caller can tell
// agent reports made after the submit from those made before it.
func TestTypeAndSubmitRetriesAndReportsFirstReturn(t *testing.T) {
	prompt := "Review PR 439 and report what you find"
	panes := []string{"› " + prompt, "› " + prompt, "• Working\n› "}
	var typed int
	var enters []time.Time
	capture := func(context.Context) (string, error) {
		p := panes[0]
		if len(panes) > 1 {
			panes = panes[1:]
		}
		return p, nil
	}
	timing := SubmitTiming{Initial: time.Millisecond, Settle: time.Millisecond, Attempts: 6}
	submitAt, err := TypeAndSubmit(context.Background(), prompt, timing,
		func(context.Context) error { typed++; return nil },
		func(context.Context) error { enters = append(enters, time.Now()); return nil },
		capture)
	if err != nil {
		t.Fatal(err)
	}
	if typed != 1 || len(enters) != 3 {
		t.Fatalf("typed %d, enters %d; want 1 and 3", typed, len(enters))
	}
	if submitAt.After(enters[0]) || enters[0].Sub(submitAt) > 50*time.Millisecond {
		t.Fatalf("submitAt %v, first Return %v: want the stamp just before the first Return", submitAt, enters[0])
	}
}

// The attempt budget caps Returns when the composer never clears.
func TestTypeAndSubmitStopsAtAttemptBudget(t *testing.T) {
	prompt := "stuck forever prompt text"
	enters := 0
	timing := SubmitTiming{Initial: time.Millisecond, Settle: time.Millisecond, Attempts: 4}
	_, err := TypeAndSubmit(context.Background(), prompt, timing,
		func(context.Context) error { return nil },
		func(context.Context) error { enters++; return nil },
		func(context.Context) (string, error) { return "› " + prompt, nil })
	if err != nil {
		t.Fatal(err)
	}
	if enters != 4 {
		t.Fatalf("enters = %d, want 4", enters)
	}
}
