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

// activityMarker separates the pane from the timings in the tmux observation, so
// one remote command can return both.
const activityMarker = "@@AIMAN_ACTIVITY@@"

// ObserveSession gathers everything the classifier needs about a session in a
// single remote call: the rendered pane, how long the session has been silent,
// and how long ago its terminal title changed.
//
// The timings matter more than the pane. `pane.Classify` has branches that
// depend on silence for telling a finished turn from a running one, but the
// dashboard only ever handed it a pane and left the durations unset — so those
// branches never fired, for tmux or PTY. tmux has always exposed
// #{session_activity}; PTY sessions had no equivalent until the holder began
// publishing one.
//
// Both facts come back with the pane rather than from a second round trip: this
// runs on every poll for the selected session, and a second SSH call per poll
// would cost more than the classification is worth.
func ObserveSession(ctx context.Context, remote PaneCapturer, s domain.Session) (pane.Observation, error) {
	if s.IsPTY() {
		return observePTYSession(ctx, remote, s)
	}
	return observeTmuxSession(ctx, remote, s)
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

// observeTmuxSession asks tmux for the pane and its last-activity stamp in one
// command. tmux tracks activity itself, so nothing has to be inferred.
//
// tmux sets no terminal title of its own that reflects the agent, so the title
// signal is left unknown here rather than guessed at.
func observeTmuxSession(ctx context.Context, remote PaneCapturer, s domain.Session) (pane.Observation, error) {
	if s.TmuxSession == "" {
		return pane.Observation{SinceOutput: -1, SinceTitleChange: -1}, nil
	}
	// "now" comes from the shell, not from a tmux format: #{t:#{now}} is not
	// supported by every tmux and returns empty, which would silently read as
	// "activity unknown". ssh.Manager.SessionActivityAges takes the same
	// approach.
	out, err := remote.Execute(ctx, fmt.Sprintf(
		"now=$(date +%%s); tmux capture-pane -p -e -S - -t %[1]q; printf '%%s\\n' %[2]q; "+
			"printf '%%s %%s\\n' \"$(tmux display-message -p -t %[1]q '#{session_activity}' 2>/dev/null)\" \"$now\"",
		s.TmuxSession, activityMarker))
	if err != nil {
		// Fall back to the plain capture so a preview still works when the
		// combined command is unavailable for any reason.
		text, cerr := remote.CaptureTmuxPane(ctx, s.TmuxSession)
		return pane.Observation{Pane: text, SinceOutput: -1, SinceTitleChange: -1}, cerr
	}
	text, since := splitTmuxObservation(out)
	return pane.Observation{Pane: text, SinceOutput: since, SinceTitleChange: -1}, nil
}

// splitTmuxObservation pulls the pane and the silence duration apart. An absent
// or unparseable stamp yields -1, which the classifier reads as unknown.
func splitTmuxObservation(out string) (string, time.Duration) {
	idx := strings.LastIndex(out, activityMarker)
	if idx < 0 {
		return out, -1
	}
	text := strings.TrimSuffix(out[:idx], "\n")
	fields := strings.Fields(out[idx+len(activityMarker):])
	if len(fields) < 2 {
		return text, -1
	}
	activity, aerr := parseUnixSeconds(fields[0])
	now, nerr := parseUnixSeconds(fields[1])
	if !aerr || !nerr || now < activity {
		return text, -1
	}
	return text, time.Duration(now-activity) * time.Second
}

func parseUnixSeconds(s string) (int64, bool) {
	var n int64
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int64(r-'0')
	}
	return n, true
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
