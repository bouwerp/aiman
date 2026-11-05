package limits

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	museTimeout = 15 * time.Second
	// maxReasonRunes keeps text a CLI printed to one short line.
	maxReasonRunes = 200
)

// NewMuseSource reads `muse usage --json`.
func NewMuseSource(env Env) Source { return museSource{run: env.Run} }

type museSource struct {
	run func(ctx context.Context, name string, args ...string) (string, int, error)
}

func (museSource) Name() string           { return "muse" }
func (museSource) MinAge() time.Duration  { return time.Minute }
func (museSource) Timeout() time.Duration { return museTimeout }

func (s museSource) Read(ctx context.Context) Reading {
	out, exit, err := s.run(ctx, "muse", "usage", "--json")
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return Reading{Provider: "muse", State: StateUnavailable}
	case errors.Is(err, context.DeadlineExceeded):
		return museUnknown("muse usage timed out")
	case err != nil:
		return museUnknown("muse usage did not run")
	case exit != 0:
		return museUnknown(fmt.Sprintf("muse usage exited %d", exit))
	}
	return parseMuseUsage(out)
}

func museUnknown(reason string) Reading {
	return Reading{Provider: "muse", State: StateUnknown, Reason: reason, Source: "muse usage"}
}

type museUsage struct {
	Allowance *struct {
		AsOfMs int64       `json:"asOfMs"`
		Tier   string      `json:"tier"`
		Window *museWindow `json:"window"`
		Weekly *museWindow `json:"weekly"`
	} `json:"allowance"`
	Hint   string `json:"hint"`
	Reason string `json:"reason"`
}

type museWindow struct {
	ResetsAtMs  int64    `json:"resetsAtMs"`
	UsedPercent *float64 `json:"usedPercent"`
}

func parseMuseUsage(out string) Reading {
	var u museUsage
	if err := json.Unmarshal([]byte(out), &u); err != nil {
		return museUnknown("could not read muse usage output")
	}
	if u.Allowance == nil {
		return museUnknown(firstReason(u.Hint, u.Reason, "muse reported no allowance"))
	}
	windows := make([]Window, 0, 2)
	windows = u.Allowance.Window.appendTo(windows, "5h")
	windows = u.Allowance.Weekly.appendTo(windows, "week")
	if len(windows) == 0 {
		return museUnknown("muse allowance has no usage figures")
	}
	r := Reading{
		Provider: "muse",
		Plan:     u.Allowance.Tier,
		Windows:  windows,
		State:    StateOK,
		Source:   "muse usage",
	}
	if u.Allowance.AsOfMs > 0 {
		r.ObservedAt = time.UnixMilli(u.Allowance.AsOfMs)
	}
	return r
}

// appendTo adds w to windows when it has a figure. A missing percent is left out
// rather than read as zero.
func (w *museWindow) appendTo(windows []Window, label string) []Window {
	if w == nil || w.UsedPercent == nil {
		return windows
	}
	out := Window{Label: label, UsedPercent: *w.UsedPercent}
	if w.ResetsAtMs > 0 {
		out.ResetsAt = time.UnixMilli(w.ResetsAtMs)
	}
	return append(windows, out)
}

// firstReason returns the first non-empty text as one short line.
func firstReason(texts ...string) string {
	for _, text := range texts {
		line := strings.Join(strings.Fields(text), " ")
		if line == "" {
			continue
		}
		if runes := []rune(line); len(runes) > maxReasonRunes {
			return string(runes[:maxReasonRunes])
		}
		return line
	}
	return ""
}
