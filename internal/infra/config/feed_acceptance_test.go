//go:build feedaccept

package config

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// Acceptance tests for event-feed piece P10 (docs/specs/event-feed.md §10).

func TestFeedConfigDefaults(t *testing.T) {
	var f FeedConfig
	if f.EnabledOrDefault() {
		t.Error("feed is on by default")
	}
	if got := f.IntervalOrDefault(); got != 5*time.Minute {
		t.Errorf("IntervalOrDefault = %v, want 5m", got)
	}
	if got := f.UrgentGapOrDefault(); got != time.Minute {
		t.Errorf("UrgentGapOrDefault = %v, want 1m", got)
	}
	if got := f.DailyBatchesOrDefault(); got != 150 {
		t.Errorf("DailyBatchesOrDefault = %d, want 150", got)
	}
	if got := f.TierOrDefault(); got != TierSmart {
		t.Errorf("TierOrDefault = %q, want %q", got, TierSmart)
	}
	if got := f.RunTimeoutOrDefault(); got != 5*time.Minute {
		t.Errorf("RunTimeoutOrDefault = %v, want 5m", got)
	}
	if got := f.MuteDaysOrDefault(); got != 7 {
		t.Errorf("MuteDaysOrDefault = %d, want 7", got)
	}
	if got := f.ActionTTLOrDefault(); got != 24*time.Hour {
		t.Errorf("ActionTTLOrDefault = %v, want 24h", got)
	}
	if got := f.RetentionOrDefault(); got != 30*24*time.Hour {
		t.Errorf("RetentionOrDefault = %v, want 30d", got)
	}
	if got := f.Slack.CallsPerMinuteOrDefault(); got != 40 {
		t.Errorf("CallsPerMinuteOrDefault = %d, want 40", got)
	}
}

func TestFeedConfigSetValues(t *testing.T) {
	on, zero, three := true, 0, 3
	f := FeedConfig{
		Enabled: &on, Interval: "10m", UrgentGap: "0", DailyBatches: &zero,
		MuteDays: &three, ActionTTL: "2h", Retention: "7d",
		Slack: FeedSlackConfig{CallsPerMinute: &three},
	}
	if !f.EnabledOrDefault() || f.IntervalOrDefault() != 10*time.Minute {
		t.Errorf("enabled %v interval %v", f.EnabledOrDefault(), f.IntervalOrDefault())
	}
	if f.UrgentGapOrDefault() != 0 {
		t.Errorf("urgent_gap 0 must turn early starts off, got %v", f.UrgentGapOrDefault())
	}
	if f.DailyBatchesOrDefault() != 0 {
		t.Errorf("daily_batches 0 is no cap, got %d", f.DailyBatchesOrDefault())
	}
	if f.MuteDaysOrDefault() != 3 || f.ActionTTLOrDefault() != 2*time.Hour || f.RetentionOrDefault() != 7*24*time.Hour {
		t.Errorf("mute %d ttl %v retention %v", f.MuteDaysOrDefault(), f.ActionTTLOrDefault(), f.RetentionOrDefault())
	}
	if f.Slack.CallsPerMinuteOrDefault() != 3 {
		t.Errorf("calls %d", f.Slack.CallsPerMinuteOrDefault())
	}
}

// An interval outside 1m..60m keeps the default, as other accessors keep the
// default for an unreadable value.
func TestFeedConfigIntervalOutOfRange(t *testing.T) {
	for _, v := range []string{"90m", "30s", "soon"} {
		if got := (FeedConfig{Interval: v}).IntervalOrDefault(); got != 5*time.Minute {
			t.Errorf("interval %q = %v, want the 5m default", v, got)
		}
	}
	for v, want := range map[string]time.Duration{"1m": time.Minute, "60m": time.Hour} {
		if got := (FeedConfig{Interval: v}).IntervalOrDefault(); got != want {
			t.Errorf("interval %q = %v, want %v", v, got, want)
		}
	}
}

func TestFeedConfigPushedToServe(t *testing.T) {
	on, calls := true, 20
	cfg := &Config{
		Remotes: []Remote{{Name: "regent0", Host: "regent0", User: "code"}},
		Feed: FeedConfig{
			Enabled: &on, Interval: "10m", Tier: "worker",
			Slack:    FeedSlackConfig{Exclude: []string{"C9"}, CallsPerMinute: &calls},
			GitHub:   FeedGitHubConfig{Repos: []string{"realfi-co/realfi"}},
			Sessions: FeedSessionsConfig{Groups: []string{"wtb"}},
		},
	}
	out, err := cfg.MarshalServeConfig("regent0")
	if err != nil {
		t.Fatal(err)
	}
	var back Config
	if err := yaml.Unmarshal(out, &back); err != nil {
		t.Fatalf("re-read: %v\n%s", err, out)
	}
	f := back.Feed
	if !f.EnabledOrDefault() || f.Interval != "10m" || f.Tier != "worker" {
		t.Fatalf("feed lost on push: %+v\n%s", f, out)
	}
	if len(f.Slack.Exclude) != 1 || f.Slack.Exclude[0] != "C9" || f.Slack.CallsPerMinuteOrDefault() != 20 {
		t.Fatalf("feed.slack lost on push: %+v\n%s", f.Slack, out)
	}
	if len(f.GitHub.Repos) != 1 || len(f.Sessions.Groups) != 1 {
		t.Fatalf("feed.github or feed.sessions lost on push: %+v\n%s", f, out)
	}
}
