package usecase

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// observeRemote records the command and returns canned output.
type observeRemote struct {
	cmds []string
	out  string
	err  error
}

func (o *observeRemote) Execute(_ context.Context, cmd string) (string, error) {
	o.cmds = append(o.cmds, cmd)
	return o.out, o.err
}
func (o *observeRemote) WriteFile(_ context.Context, _ string, _ []byte) error { return nil }

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
