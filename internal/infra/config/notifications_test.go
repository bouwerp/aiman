package config

import (
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestNotificationsDefaults(t *testing.T) {
	var n NotificationsConfig
	if got := n.EventKinds(); len(got) != 5 {
		t.Fatalf("events %v", got)
	}
	if !n.PhoneEnabled() {
		t.Fatal("phone default must be true")
	}
	if n.MinIntervalDuration() != 2*time.Minute {
		t.Fatal("min_interval default 2m")
	}
	n.MinInterval = "bogus"
	if n.MinIntervalDuration() != 2*time.Minute {
		t.Fatal("bad duration falls back to 2m")
	}
	n.Events = []string{"ended", "nope"}
	if got := n.EventKinds(); len(got) != 1 || got[0] != domain.NotifyEnded {
		t.Fatalf("got %v", got)
	}
}

func TestNotificationsForMergesRemoteOverride(t *testing.T) {
	off := false
	cfg := &Config{
		Notifications: NotificationsConfig{Webhook: "https://ntfy.sh/a", MinInterval: "5m"},
		Remotes:       []Remote{{Host: "r1", Notifications: &NotificationsConfig{Phone: &off, SlackChannel: "#ops"}}},
	}
	got := cfg.NotificationsFor("r1")
	if got.PhoneEnabled() || got.SlackChannel != "#ops" || got.Webhook != "https://ntfy.sh/a" || got.MinInterval != "5m" {
		t.Fatalf("merge wrong: %+v", got)
	}
	if !cfg.NotificationsFor("other").PhoneEnabled() {
		t.Fatal("other host keeps global")
	}
}
