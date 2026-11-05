package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bouwerp/aiman/internal/limits"
	"github.com/bouwerp/aiman/internal/server"
)

const (
	// limitsPollEvery is how often the CLI asks while serve is refreshing.
	limitsPollEvery = 2 * time.Second
	// limitsWaitFor outlasts the slowest source, the 30-second Grok probe.
	limitsWaitFor = 40 * time.Second
	// claudeStatusInputMax bounds what record-claude reads from a status line.
	claudeStatusInputMax = 4 << 20
)

// runLimits is `aiman limits`, the limits.get call, plus the record-claude step
// that the Claude status line wrapper runs.
func runLimits(args []string, stdin io.Reader, out io.Writer) error {
	if len(args) > 0 && args[0] == "record-claude" {
		return runLimitsRecordClaude(stdin)
	}
	flags, rest := takeFlags(args)
	if len(rest) != 0 {
		fmt.Fprintf(os.Stderr, "Usage: aiman limits [--refresh] [--json]\n")
		return errUsage
	}
	sock, err := socketPath()
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return err
	}
	snap, err := waitForLimits(flags["refresh"] == "1", time.Sleep, func(refresh bool) (limits.Snapshot, error) {
		return callLimits(sock, refresh)
	})
	if err != nil {
		return err
	}
	if flags["json"] == "1" {
		return writeJSON(snap)
	}
	return formatLimits(out, snap, time.Now())
}

// runLimitsRecordClaude stores the rate limits from status line JSON on stdin.
func runLimitsRecordClaude(stdin io.Reader) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("home directory: %w", err)
	}
	input, err := io.ReadAll(io.LimitReader(stdin, claudeStatusInputMax))
	if err != nil {
		return fmt.Errorf("read status line input: %w", err)
	}
	if err := limits.RecordClaude(input, home, time.Now()); err != nil {
		return fmt.Errorf("record claude limits: %w", err)
	}
	return nil
}

// waitForLimits asks once, with refresh as given, then polls until serve has
// finished or the wait runs out. Serve never blocks on a provider, so the first
// answer on a fresh serve holds no readings. It returns what it has at the limit.
func waitForLimits(refresh bool, sleep func(time.Duration), get func(refresh bool) (limits.Snapshot, error)) (limits.Snapshot, error) {
	snap, err := get(refresh)
	for waited := time.Duration(0); err == nil && snap.Refreshing && waited < limitsWaitFor; waited += limitsPollEvery {
		sleep(limitsPollEvery)
		snap, err = get(false)
	}
	return snap, err
}

func callLimits(sock string, refresh bool) (limits.Snapshot, error) {
	resp, err := server.Call(sock, "limits.get", map[string]any{"refresh": refresh})
	if err != nil {
		code := server.CodeServerNotRunning
		if !errors.Is(err, server.ErrServerNotRunning) {
			code = server.CodeInvalidParams
		}
		writeCLIError(code, err.Error())
		return limits.Snapshot{}, err
	}
	if resp.Error != nil {
		writeCLIResponseError(resp.Error)
		return limits.Snapshot{}, errors.New(resp.Error.Message)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		return limits.Snapshot{}, fmt.Errorf("limits result: %w", err)
	}
	var snap limits.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return limits.Snapshot{}, fmt.Errorf("limits result: %w", err)
	}
	return snap, nil
}

func formatLimits(w io.Writer, snap limits.Snapshot, now time.Time) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, r := range snap.Readings {
		// A tool that is simply not installed has nothing to say.
		if r.State == limits.StateUnavailable && r.Reason == "" {
			continue
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Provider, r.Plan, limitsDetail(r), limitsAge(r, now)); err != nil {
			return fmt.Errorf("write limits: %w", err)
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write limits: %w", err)
	}
	if snap.Refreshing {
		if _, err := io.WriteString(w, "still refreshing; run again for the rest\n"); err != nil {
			return fmt.Errorf("write limits: %w", err)
		}
	}
	return nil
}

// limitsDetail is one reading's numbers, followed by why they may be stale.
func limitsDetail(r limits.Reading) string {
	parts := make([]string, 0, len(r.Windows)+len(r.Balances)+2)
	for _, win := range r.Windows {
		parts = append(parts, limitsWindow(win))
	}
	for _, b := range r.Balances {
		parts = append(parts, strings.TrimSpace(b.Amount+" "+b.Currency))
	}
	if r.Spend != "" {
		parts = append(parts, r.Spend)
	}
	if r.State != limits.StateOK {
		parts = append(parts, strings.TrimSpace(string(r.State)+": "+r.Reason))
	}
	return strings.Join(parts, ", ")
}

func limitsWindow(win limits.Window) string {
	if win.Reset {
		return win.Label + " reset"
	}
	text := win.Label + " " + strconv.FormatFloat(win.UsedPercent, 'f', -1, 64) + "%"
	switch {
	case !win.ResetsAt.IsZero():
		return text + " (resets " + win.ResetsAt.Local().Format("Jan 2 15:04") + ")"
	case win.ResetsText != "":
		return text + " (resets " + win.ResetsText + ")"
	}
	return text
}

func limitsAge(r limits.Reading, now time.Time) string {
	if r.ObservedAt.IsZero() {
		return ""
	}
	age := now.Sub(r.ObservedAt)
	switch {
	case age < time.Minute:
		return "now"
	case age < time.Hour:
		return strconv.Itoa(int(age.Minutes())) + "m ago"
	case age < 48*time.Hour:
		return strconv.Itoa(int(age.Hours())) + "h ago"
	}
	return strconv.Itoa(int(age.Hours()/24)) + "d ago"
}
