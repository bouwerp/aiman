package ui

import (
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/infra/config"
)

func TestApplyFeedSettingKeys(t *testing.T) {
	var f config.FeedConfig
	for key, val := range map[string]string{
		"enabled": "on", "daily_batches": "0", "tier": "worker", "slack.exclude": "C1, C2",
		"retention": "14d", "urgent_gap": "0", "mute_days": "3",
	} {
		if err := applyFeedSetting(&f, key, val); err != nil {
			t.Fatalf("%s=%s: %v", key, val, err)
		}
	}
	if !f.EnabledOrDefault() || f.DailyBatchesOrDefault() != 0 || f.Tier != "worker" ||
		len(f.Slack.Exclude) != 2 || f.RetentionOrDefault() != 14*24*time.Hour || f.UrgentGapOrDefault() != 0 || f.MuteDaysOrDefault() != 3 {
		t.Fatalf("stored %+v", f)
	}
	for key, val := range map[string]string{"tier": "huge", "mute_days": "0", "enabled": "maybe", "nope": "1", "action_ttl": "-1h"} {
		if err := applyFeedSetting(&f, key, val); err == nil {
			t.Errorf("%s=%s accepted", key, val)
		}
	}
	if err := applyFeedSetting(&f, "enabled", ""); err != nil || f.Enabled != nil {
		t.Fatalf("empty did not clear enabled: %v %v", err, f.Enabled)
	}
}

func TestFeedProjectedRuns(t *testing.T) {
	noon := time.Date(2026, 11, 26, 12, 0, 0, 0, time.Local)
	var f config.FeedConfig
	if p, c := feedProjectedRuns(f, 30, noon); p != 60 || c != 150 {
		t.Fatalf("projected %d cap %d, want 60 and 150", p, c)
	}
	f.Interval = "60m"
	if p, c := feedProjectedRuns(f, 30, noon); p != 24 || c != 24 {
		t.Fatalf("projected %d cap %d, want 24 and 24", p, c)
	}
}
