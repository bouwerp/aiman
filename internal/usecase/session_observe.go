package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/pane"
)

// ObserveSession gathers everything the classifier needs about a session in a
// single remote call: the rendered pane, how long the session has been silent,
// and how long ago its terminal title changed.
//
// The timings matter more than the pane. `pane.Classify` has branches that
// depend on silence for telling a finished turn from a running one, so the
// PTY holder publishes both stamps with the pane. Both come back with the
// pane rather than from a second round trip: this runs on every poll for the
// selected session. A legacy tmux row returns its *domain.LegacyTmuxError.
func ObserveSession(ctx context.Context, remote TerminalExecutor, s domain.Session) (pane.Observation, error) {
	if s.IsLegacyTmux() {
		return pane.Observation{}, domain.NewLegacyTmuxError(s)
	}
	return observePTYSession(ctx, remote, s)
}

// ptyCaptureResult is the shape `pty.capture` returns. The activity fields are
// absent on an older serve, which reads as "unknown" and falls back to the
// pane-only signals.
type ptyCaptureResult struct {
	Text         string `json:"text"`
	LastOutput   string `json:"last_output"`
	TitleChanged string `json:"title_changed_at"`
	Title        string `json:"title"`
}

func observePTYSession(ctx context.Context, remote TerminalExecutor, s domain.Session) (pane.Observation, error) {
	out, err := remote.Execute(ctx, remoteAimanPreamble+fmt.Sprintf(
		"aiman pty capture %q --lines 0", terminalID(s)))
	if err != nil {
		return pane.Observation{}, fmt.Errorf("pty capture: %w", err)
	}

	obs := pane.Observation{SinceOutput: -1, SinceTitleChange: -1}
	var res ptyCaptureResult
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); jerr != nil {
		// Not JSON: an older or partial response. The text is still useful.
		obs.Pane = out
		return obs, nil
	}
	obs.Pane = res.Text
	now := time.Now()
	if since, ok := ageOf(res.LastOutput, now); ok {
		obs.SinceOutput = since
	}
	if since, ok := ageOf(res.TitleChanged, now); ok {
		obs.SinceTitleChange = since
	}
	return obs, nil
}

// ageOf turns a published RFC3339 timestamp into an age. A clock skew between
// hosts can make a remote stamp look like the future; that is reported as
// unknown rather than as a negative age the classifier would misread.
func ageOf(stamp string, now time.Time) (time.Duration, bool) {
	if stamp == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return 0, false
	}
	age := now.Sub(t)
	if age < 0 {
		return 0, false
	}
	return age, true
}
