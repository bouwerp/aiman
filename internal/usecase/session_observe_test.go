package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// observeRemote records the command and returns canned output.
type observeRemote struct {
	cmds     []string
	out      string
	err      error
	fallback string
}

func (o *observeRemote) Execute(_ context.Context, cmd string) (string, error) {
	o.cmds = append(o.cmds, cmd)
	return o.out, o.err
}
func (o *observeRemote) WriteFile(_ context.Context, _ string, _ []byte) error { return nil }
func (o *observeRemote) CaptureTmuxPane(_ context.Context, _ string) (string, error) {
	return o.fallback, nil
}

// The classifier reasons about silence, but the dashboard only ever handed it a
// pane — so those branches never fired. One call now returns both.
func TestObserveTmuxSessionReturnsPaneAndSilence(t *testing.T) {
	activity := time.Now().Add(-90 * time.Second).Unix()
	now := time.Now().Unix()
	r := &observeRemote{out: fmt.Sprintf("pane line one\npane line two\n%s\n%d %d\n",
		activityMarker, activity, now)}

	obs, err := ObserveSession(context.Background(), r, domain.Session{TmuxSession: "demo"})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Pane != "pane line one\npane line two" {
		t.Errorf("pane = %q", obs.Pane)
	}
	if obs.SinceOutput < 80*time.Second || obs.SinceOutput > 100*time.Second {
		t.Errorf("silence = %s, want about 90s", obs.SinceOutput)
	}
	// tmux sets no title reflecting the agent, so that stays unknown rather than
	// being guessed at.
	if obs.SinceTitleChange >= 0 {
		t.Errorf("title age should be unknown for tmux, got %s", obs.SinceTitleChange)
	}
	if len(r.cmds) != 1 {
		t.Errorf("expected one round trip, got %d", len(r.cmds))
	}
}

// An older tmux, or any missing stamp, must degrade to "unknown" rather than
// inventing a duration.
func TestObserveTmuxSessionToleratesAMissingStamp(t *testing.T) {
	for _, out := range []string{
		"just a pane, no marker",
		"pane\n" + activityMarker + "\n\n",           // marker but empty stamp
		"pane\n" + activityMarker + "\nnotanumber x", // unparseable
	} {
		r := &observeRemote{out: out}
		obs, err := ObserveSession(context.Background(), r, domain.Session{TmuxSession: "demo"})
		if err != nil {
			t.Fatalf("observe: %v", err)
		}
		if obs.SinceOutput != -1 {
			t.Errorf("out %q: silence = %s, want unknown", out, obs.SinceOutput)
		}
		if !strings.Contains(obs.Pane, "pane") {
			t.Errorf("out %q: pane lost: %q", out, obs.Pane)
		}
	}
}

// If the combined command fails, a preview must still work.
func TestObserveTmuxSessionFallsBackToAPlainCapture(t *testing.T) {
	r := &observeRemote{err: errors.New("no"), fallback: "plain pane"}
	obs, err := ObserveSession(context.Background(), r, domain.Session{TmuxSession: "demo"})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Pane != "plain pane" {
		t.Errorf("pane = %q, want the fallback capture", obs.Pane)
	}
	if obs.SinceOutput != -1 || obs.SinceTitleChange != -1 {
		t.Errorf("timings should be unknown on the fallback path")
	}
}

func TestObservePTYSessionReadsHolderActivity(t *testing.T) {
	lastOut := time.Now().Add(-3 * time.Second).UTC().Format(time.RFC3339Nano)
	titleAt := time.Now().Add(-1 * time.Second).UTC().Format(time.RFC3339Nano)
	r := &observeRemote{out: fmt.Sprintf(
		`{"type":"pane_read","text":"the screen","last_output":%q,"title_changed_at":%q,"title":"◐ task"}`,
		lastOut, titleAt)}

	obs, err := ObserveSession(context.Background(), r, domain.Session{ID: "s", Backend: domain.BackendPTY})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Pane != "the screen" {
		t.Errorf("pane = %q", obs.Pane)
	}
	if obs.SinceOutput <= 0 || obs.SinceOutput > 10*time.Second {
		t.Errorf("silence = %s, want about 3s", obs.SinceOutput)
	}
	if obs.SinceTitleChange <= 0 || obs.SinceTitleChange > 10*time.Second {
		t.Errorf("title age = %s, want about 1s", obs.SinceTitleChange)
	}
	if len(r.cmds) != 1 {
		t.Errorf("expected one round trip, got %d", len(r.cmds))
	}
}

// A serve that predates the activity fields returns the pane alone; that must
// read as "unknown timings", not as zero durations the classifier would misread
// as "just now".
func TestObservePTYSessionWithoutActivityFields(t *testing.T) {
	r := &observeRemote{out: `{"type":"pane_read","text":"the screen"}`}
	obs, err := ObserveSession(context.Background(), r, domain.Session{ID: "s", Backend: domain.BackendPTY})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Pane != "the screen" {
		t.Errorf("pane = %q", obs.Pane)
	}
	if obs.SinceOutput != -1 || obs.SinceTitleChange != -1 {
		t.Errorf("missing fields must read as unknown, got %s / %s", obs.SinceOutput, obs.SinceTitleChange)
	}
}

// Clocks on two hosts do not agree exactly, so a stamp can look like the future.
// That is unknown, not a negative age.
func TestAgeOfRejectsFutureStamps(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Minute).UTC().Format(time.RFC3339Nano)
	if _, ok := ageOf(future, now); ok {
		t.Error("a future stamp must report unusable rather than a negative age")
	}
	past := now.Add(-time.Second).UTC().Format(time.RFC3339Nano)
	if age, ok := ageOf(past, now); !ok || age <= 0 {
		t.Errorf("a past stamp should give a positive age, got %s / %v", age, ok)
	}
	if _, ok := ageOf("", now); ok {
		t.Error("an empty stamp is unknown")
	}
	if _, ok := ageOf("not a time", now); ok {
		t.Error("an unparseable stamp is unknown")
	}
}
