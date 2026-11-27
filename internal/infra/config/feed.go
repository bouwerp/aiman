package config

import (
	"strconv"
	"strings"
	"time"
)

// Feed defaults (docs/specs/event-feed.md §10).
const (
	DefaultFeedInterval       = 5 * time.Minute
	DefaultFeedUrgentGap      = time.Minute
	DefaultFeedDailyBatches   = 150
	DefaultFeedRunTimeout     = 5 * time.Minute
	DefaultFeedMuteDays       = 7
	DefaultFeedActionTTL      = 24 * time.Hour
	DefaultFeedRetention      = 30 * 24 * time.Hour
	DefaultFeedSlackCallsPerM = 40
	minFeedInterval           = time.Minute
	maxFeedInterval           = time.Hour
)

// FeedConfig configures serve's event feed.
type FeedConfig struct {
	Enabled      *bool              `yaml:"enabled,omitempty"`
	Interval     string             `yaml:"interval,omitempty"`
	UrgentGap    string             `yaml:"urgent_gap,omitempty"`
	DailyBatches *int               `yaml:"daily_batches,omitempty"`
	Tier         string             `yaml:"tier,omitempty"`
	Agent        string             `yaml:"agent,omitempty"`
	Model        string             `yaml:"model,omitempty"`
	RunTimeout   string             `yaml:"run_timeout,omitempty"`
	MuteDays     *int               `yaml:"mute_days,omitempty"`
	ActionTTL    string             `yaml:"action_ttl,omitempty"`
	Retention    string             `yaml:"retention,omitempty"`
	Slack        FeedSlackConfig    `yaml:"slack,omitempty"`
	GitHub       FeedGitHubConfig   `yaml:"github,omitempty"`
	Sessions     FeedSessionsConfig `yaml:"sessions,omitempty"`
}

// FeedSlackConfig narrows what the feed collects from Slack.
type FeedSlackConfig struct {
	Enabled        *bool    `yaml:"enabled,omitempty"`
	Exclude        []string `yaml:"exclude,omitempty"`
	Bots           []string `yaml:"bots,omitempty"`
	CallsPerMinute *int     `yaml:"calls_per_minute,omitempty"`
}

// FeedGitHubConfig narrows what the feed collects from GitHub.
type FeedGitHubConfig struct {
	Enabled *bool    `yaml:"enabled,omitempty"`
	Repos   []string `yaml:"repos,omitempty"`
}

// FeedSessionsConfig narrows which sessions the feed reads. Enabled
// switches the aiman source (session notifications and ledger rows).
type FeedSessionsConfig struct {
	Enabled *bool    `yaml:"enabled,omitempty"`
	Groups  []string `yaml:"groups,omitempty"`
}

// EnabledOrDefault reports whether the feed runs; unset is off.
func (f FeedConfig) EnabledOrDefault() bool { return f.Enabled != nil && *f.Enabled }

// Feed source names, as the collector stores them on events.
const (
	FeedSourceAiman  = "aiman"
	FeedSourceGitHub = "github"
	FeedSourceSlack  = "slack"
)

// SourceOn reports whether a feed source runs: unset is on, and a name this
// config has no switch for is on.
func (f FeedConfig) SourceOn(source string) bool {
	var p *bool
	switch source {
	case FeedSourceAiman:
		p = f.Sessions.Enabled
	case FeedSourceGitHub:
		p = f.GitHub.Enabled
	case FeedSourceSlack:
		p = f.Slack.Enabled
	}
	return p == nil || *p
}

// OffSources lists the sources switched off.
func (f FeedConfig) OffSources() []string {
	var out []string
	for _, s := range []string{FeedSourceAiman, FeedSourceGitHub, FeedSourceSlack} {
		if !f.SourceOn(s) {
			out = append(out, s)
		}
	}
	return out
}

// IntervalOrDefault is the batch interval; a value outside 1m..60m or one
// that does not parse keeps the default.
func (f FeedConfig) IntervalOrDefault() time.Duration {
	d, ok := parseFeedDuration(f.Interval)
	if !ok || d < minFeedInterval || d > maxFeedInterval {
		return DefaultFeedInterval
	}
	return d
}

// UrgentGapOrDefault is the shortest gap before an urgent early batch; "0"
// turns early starts off.
func (f FeedConfig) UrgentGapOrDefault() time.Duration {
	d, ok := parseFeedDuration(f.UrgentGap)
	if !ok || d < 0 {
		return DefaultFeedUrgentGap
	}
	return d
}

// DailyBatchesOrDefault caps batches per day; 0 is no cap.
func (f FeedConfig) DailyBatchesOrDefault() int {
	if f.DailyBatches == nil || *f.DailyBatches < 0 {
		return DefaultFeedDailyBatches
	}
	return *f.DailyBatches
}

// TierOrDefault is the model tier the feed agent is picked from.
func (f FeedConfig) TierOrDefault() string {
	if t := strings.TrimSpace(f.Tier); ValidTier(t) {
		return t
	}
	return TierSmart
}

// RunTimeoutOrDefault bounds one feed job.
func (f FeedConfig) RunTimeoutOrDefault() time.Duration {
	if d, ok := parseFeedDuration(f.RunTimeout); ok && d > 0 {
		return d
	}
	return DefaultFeedRunTimeout
}

// MuteDaysOrDefault is how long a mute lasts, in days.
func (f FeedConfig) MuteDaysOrDefault() int {
	if f.MuteDays == nil || *f.MuteDays <= 0 {
		return DefaultFeedMuteDays
	}
	return *f.MuteDays
}

// ActionTTLOrDefault is how long an open action stays before it expires.
func (f FeedConfig) ActionTTLOrDefault() time.Duration {
	if d, ok := parseFeedDuration(f.ActionTTL); ok && d > 0 {
		return d
	}
	return DefaultFeedActionTTL
}

// RetentionOrDefault is how long feed events are kept.
func (f FeedConfig) RetentionOrDefault() time.Duration {
	if d, ok := parseFeedDuration(f.Retention); ok && d > 0 {
		return d
	}
	return DefaultFeedRetention
}

// CallsPerMinuteOrDefault caps the collector's Slack calls per minute.
func (s FeedSlackConfig) CallsPerMinuteOrDefault() int {
	if s.CallsPerMinute == nil || *s.CallsPerMinute <= 0 {
		return DefaultFeedSlackCallsPerM
	}
	return *s.CallsPerMinute
}

// parseFeedDuration reads a Go duration, a "<n>d" day count, or "0".
func parseFeedDuration(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if n, ok := strings.CutSuffix(v, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil || days < 0 {
			return 0, false
		}
		return time.Duration(days) * 24 * time.Hour, true
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, false
	}
	return d, true
}
