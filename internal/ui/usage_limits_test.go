package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/limits"
)

func TestUsageLimitsRows(t *testing.T) {
	now := time.Date(2026, 11, 5, 21, 0, 0, 0, time.UTC)
	rows := limitRows([]limits.Reading{
		{Provider: "codex", Plan: "plus", State: limits.StateOK, ObservedAt: now.Add(-2 * time.Hour), Windows: []limits.Window{
			{Label: "5h", Reset: true}, {Label: "week", UsedPercent: 30, ResetsAt: now.Add(48 * time.Hour)},
		}},
		{Provider: "grok", Plan: "SuperGrok Plus", State: limits.StateOK, ObservedAt: now, Windows: []limits.Window{{Label: "week", UsedPercent: 19, ResetsText: "November 11, 13:44"}}},
		{Provider: "deepseek", State: limits.StateOK, ObservedAt: now, Balances: []limits.Balance{{Amount: "12.10", Currency: "USD"}}},
		{Provider: "opencode", State: limits.StateOK, ObservedAt: now, Spend: "$3.00 in 7 days"},
		{Provider: "muse", State: limits.StateUnknown, Reason: "no usage yet in this window"},
		{Provider: "claude", State: limits.StateUnavailable, Reason: "off: ~/.aiman/limits/claude-statusline.off exists"},
		{Provider: "kept", State: limits.StateUnknown, Reason: "screen changed", ObservedAt: now.Add(-3 * time.Hour), Windows: []limits.Window{{Label: "week", UsedPercent: 5}}},
	}, now)
	all := strings.Join(rows, "\n")
	for _, want := range []string{
		"70% left", "2h", "November 11, 13:44", "81% left", "SuperGrok Plus", "12.10", "USD", "$3.00 in 7 days",
		"no usage yet in this window", "claude-statusline.off", "screen changed", "3h", "95% left",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("rows missing %q:\n%s", want, all)
		}
	}
	for _, row := range rows {
		if strings.Contains(row, "codex") && !strings.Contains(strings.ToLower(row), "reset") {
			t.Errorf("a window past its reset must say so: %q", row)
		}
	}
	if len(rows) != 7 {
		t.Fatalf("%d rows, want one per provider:\n%s", len(rows), all)
	}
}

func TestUsageLimitsRowsDropUnavailableWithNoReason(t *testing.T) {
	rows := limitRows([]limits.Reading{{Provider: "copilot", State: limits.StateUnavailable}}, time.Now())
	if len(rows) != 0 {
		t.Fatalf("a provider that does not apply here is noise: %v", rows)
	}
}

func TestUsageLimitsLineShowsWhatIsLeftForTheSession(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	if got := renderLimitsLine(nil, now, 80); got != "" {
		t.Fatalf("no reading drew %q", got)
	}
	line := renderLimitsLine(&limits.Reading{
		Provider: "claude", State: limits.StateOK, ObservedAt: now,
		Windows: []limits.Window{{Label: "week", UsedPercent: 12.5, ResetsAt: now.Add(72 * time.Hour)}},
	}, now, 80)
	for _, want := range []string{"claude", "week 87.5% left", "resets in 3d"} {
		if !strings.Contains(line, want) {
			t.Errorf("line missing %q: %q", want, line)
		}
	}
}
