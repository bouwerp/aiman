package limits

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bouwerp/aiman/internal/fsutil"
)

const claudeProvider = "claude"

// claudeSourceName marks a reading as coming from the status line record.
const claudeSourceName = "statusline"

// claudeWindowInput is one window of status line rate_limits. The pointer tells
// a missing figure from a real 0.
type claudeWindowInput struct {
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       float64  `json:"resets_at"`
}

// claudeRateLimits is the shape of rate_limits, both in status line input and
// in the recorded file.
type claudeRateLimits struct {
	FiveHour *claudeWindowInput `json:"five_hour"`
	SevenDay *claudeWindowInput `json:"seven_day"`
}

func (w *claudeWindowInput) usable() bool { return w != nil && w.UsedPercentage != nil }

func (l claudeRateLimits) usable() bool { return l.FiveHour.usable() || l.SevenDay.usable() }

// claudeRecord is all that reaches disk. Status line input also carries the
// working directory, transcript path, session id and cost, which must not.
type claudeRecord struct {
	RateLimits json.RawMessage `json:"rate_limits"`
	ObservedAt time.Time       `json:"observed_at"`
}

// parseRateLimits decodes raw rate_limits and refuses them unless at least one
// window has a figure.
func parseRateLimits(raw json.RawMessage) (claudeRateLimits, error) {
	var limits claudeRateLimits
	if len(raw) == 0 {
		return limits, errors.New("no rate_limits")
	}
	if err := json.Unmarshal(raw, &limits); err != nil {
		return limits, fmt.Errorf("parsing rate_limits: %w", err)
	}
	if !limits.usable() {
		return limits, errors.New("rate_limits has no used_percentage")
	}
	return limits, nil
}

func claudeRecordPath(home string) string {
	return filepath.Join(home, ".aiman", "limits", "claude-statusline.json")
}

// RecordClaude stores only rate_limits from status line input, atomically at
// 0600. Input without usable figures returns an error and leaves the previous
// file.
func RecordClaude(input []byte, home string, now time.Time) error {
	var in claudeRecord
	if err := json.Unmarshal(input, &in); err != nil {
		return fmt.Errorf("parsing status line input: %w", err)
	}
	if _, err := parseRateLimits(in.RateLimits); err != nil {
		return err
	}
	body, err := json.Marshal(claudeRecord{RateLimits: in.RateLimits, ObservedAt: now.UTC()})
	if err != nil {
		return fmt.Errorf("encoding record: %w", err)
	}
	path := claudeRecordPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating limits directory: %w", err)
	}
	if _, err := fsutil.WriteIfChanged(path, body, 0o600); err != nil {
		return fmt.Errorf("writing record: %w", err)
	}
	return nil
}

// NewClaudeSource reads what the status line wrapper recorded. Disabled means
// limits.claude_statusline is off.
func NewClaudeSource(env Env, enabled bool) Source {
	return claudeSource{home: env.Home, enabled: enabled}
}

type claudeSource struct {
	home    string
	enabled bool
}

func (s claudeSource) Name() string           { return claudeProvider }
func (s claudeSource) MinAge() time.Duration  { return time.Minute }
func (s claudeSource) Timeout() time.Duration { return 5 * time.Second }

// Read reports the file as recorded. The Manager applies the reset rule. Reasons
// are fixed strings, so nothing from the file can reach them.
func (s claudeSource) Read(context.Context) Reading {
	if !s.enabled {
		return Reading{Provider: claudeProvider, State: StateUnavailable, Reason: "off: set limits.claude_statusline"}
	}
	body, err := os.ReadFile(claudeRecordPath(s.home))
	if errors.Is(err, os.ErrNotExist) {
		return claudeUnknown("nothing recorded yet: waits for a Claude response from a subscriber")
	}
	if err != nil {
		return claudeUnknown("could not read the recorded status line")
	}
	var rec claudeRecord
	if err := json.Unmarshal(body, &rec); err != nil || rec.ObservedAt.IsZero() {
		return claudeUnknown("the recorded status line is corrupt")
	}
	limits, err := parseRateLimits(rec.RateLimits)
	if err != nil {
		return claudeUnknown("the recorded status line has no usable limits")
	}
	return Reading{
		Provider:   claudeProvider,
		State:      StateOK,
		Windows:    claudeWindows(limits),
		ObservedAt: rec.ObservedAt,
		Source:     claudeSourceName,
	}
}

func claudeUnknown(reason string) Reading {
	return Reading{Provider: claudeProvider, State: StateUnknown, Reason: reason, Source: claudeSourceName}
}

func claudeWindows(limits claudeRateLimits) []Window {
	var out []Window
	for _, w := range []struct {
		label string
		input *claudeWindowInput
	}{{"5h", limits.FiveHour}, {"week", limits.SevenDay}} {
		if !w.input.usable() {
			continue
		}
		window := Window{Label: w.label, UsedPercent: *w.input.UsedPercentage}
		if w.input.ResetsAt > 0 {
			window.ResetsAt = time.Unix(int64(w.input.ResetsAt), 0)
		}
		out = append(out, window)
	}
	return out
}
