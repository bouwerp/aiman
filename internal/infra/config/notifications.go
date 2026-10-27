package config

import (
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// NotificationsConfig controls notification delivery.
type NotificationsConfig struct {
	Events       []string `yaml:"events,omitempty"`
	Phone        *bool    `yaml:"phone,omitempty"`
	Webhook      string   `yaml:"webhook,omitempty"`
	SlackChannel string   `yaml:"slack_channel,omitempty"`
	MinInterval  string   `yaml:"min_interval,omitempty"`
}

// EventKinds maps config events to domain kinds.
func (n NotificationsConfig) EventKinds() []domain.NotifyKind {
	if len(n.Events) == 0 {
		return domain.AllNotifyKinds()
	}
	allowed := map[string]bool{}
	for _, k := range domain.AllNotifyKinds() {
		allowed[string(k)] = true
	}
	var out []domain.NotifyKind
	for _, e := range n.Events {
		if allowed[e] {
			out = append(out, domain.NotifyKind(e))
		}
	}
	return out
}

// PhoneEnabled reports whether phone push is enabled.
func (n NotificationsConfig) PhoneEnabled() bool {
	if n.Phone == nil {
		return true
	}
	return *n.Phone
}

// MinIntervalDuration parses the minimum interval between notifications.
func (n NotificationsConfig) MinIntervalDuration() time.Duration {
	if n.MinInterval == "" {
		return 2 * time.Minute
	}
	d, err := time.ParseDuration(n.MinInterval)
	if err != nil || d < 0 {
		return 2 * time.Minute
	}
	return d
}
