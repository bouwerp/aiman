package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/bouwerp/aiman/internal/infra/config"
)

// feedSetting is one row of the Feed settings page.
type feedSetting struct {
	key, def, meaning string
}

// feedSettings are the keys of the feed block (docs/designs/event-feed.md
// Settings), in the page's order.
var feedSettings = []feedSetting{
	{"enabled", "false", "turn the feed on (serve's manager and the aiman-feed unit)"},
	{"interval", "5m", "how often a batch can run, 1m to 60m"},
	{"urgent_gap", "1m", "shortest gap before an urgent event starts a batch; 0 is off"},
	{"daily_batches", "150", "cap on runs per day; 0 is no cap"},
	{"tier", "smart", "tier the batch agent is picked from (smart or worker)"},
	{"agent", "", "a named agent instead of the tier"},
	{"model", "", "the named agent's model"},
	{"run_timeout", "5m", "limit for one run"},
	{"slack.exclude", "none", "channel ids never read, comma separated"},
	{"slack.calls_per_minute", "40", "budget of Slack history and replies calls"},
	{"slack.bots", "none", "bot ids whose messages count as events"},
	{"github.repos", "none", "repositories for PR and push events"},
	{"sessions.groups", "all", "groups whose root sessions count"},
	{"mute_days", "7", "length of a mute, in days"},
	{"retention", "30d", "how long events no open action names are kept"},
	{"action_ttl", "24h", "when an unanswered action expires"},
}

// applyFeedSetting sets one feed key from the page's text. An empty value
// clears the key, so the default applies. A value that does not fit leaves
// f unchanged and returns why.
func applyFeedSetting(f *config.FeedConfig, key, value string) error {
	v := strings.TrimSpace(value)
	switch key {
	case "enabled":
		if v == "" {
			f.Enabled = nil
			return nil
		}
		b, err := parseFeedBool(v)
		if err != nil {
			return err
		}
		f.Enabled = &b
	case "interval":
		if v != "" {
			d, ok := parseFeedSettingDuration(v)
			if !ok || d < time.Minute || d > time.Hour {
				return fmt.Errorf("interval %q: want a duration from 1m to 60m", v)
			}
		}
		f.Interval = v
	case "urgent_gap":
		if v != "" {
			if d, ok := parseFeedSettingDuration(v); !ok || d < 0 {
				return fmt.Errorf("urgent_gap %q: want a duration, or 0 for off", v)
			}
		}
		f.UrgentGap = v
	case "run_timeout", "action_ttl", "retention":
		if v != "" {
			if d, ok := parseFeedSettingDuration(v); !ok || d <= 0 {
				return fmt.Errorf("%s %q: want a positive duration such as 5m, 24h or 30d", key, v)
			}
		}
		switch key {
		case "run_timeout":
			f.RunTimeout = v
		case "action_ttl":
			f.ActionTTL = v
		default:
			f.Retention = v
		}
	case "daily_batches":
		n, err := parseFeedInt(key, v, 0)
		if err != nil {
			return err
		}
		f.DailyBatches = n
	case "mute_days":
		n, err := parseFeedInt(key, v, 1)
		if err != nil {
			return err
		}
		f.MuteDays = n
	case "slack.calls_per_minute":
		n, err := parseFeedInt(key, v, 1)
		if err != nil {
			return err
		}
		f.Slack.CallsPerMinute = n
	case "tier":
		if v != "" && !config.ValidTier(v) {
			return fmt.Errorf("tier %q: want %s or %s", v, config.TierSmart, config.TierWorker)
		}
		f.Tier = v
	case "agent":
		f.Agent = v
	case "model":
		f.Model = v
	case "slack.exclude":
		f.Slack.Exclude = splitFeedList(v)
	case "slack.bots":
		f.Slack.Bots = splitFeedList(v)
	case "github.repos":
		f.GitHub.Repos = splitFeedList(v)
	case "sessions.groups":
		f.Sessions.Groups = splitFeedList(v)
	default:
		return fmt.Errorf("unknown feed setting %q", key)
	}
	return nil
}

// feedSettingValue is the key's stored text, "" when unset.
func feedSettingValue(f config.FeedConfig, key string) string {
	intText := func(p *int) string {
		if p == nil {
			return ""
		}
		return strconv.Itoa(*p)
	}
	switch key {
	case "enabled":
		if f.Enabled == nil {
			return ""
		}
		return strconv.FormatBool(*f.Enabled)
	case "interval":
		return f.Interval
	case "urgent_gap":
		return f.UrgentGap
	case "daily_batches":
		return intText(f.DailyBatches)
	case "tier":
		return f.Tier
	case "agent":
		return f.Agent
	case "model":
		return f.Model
	case "run_timeout":
		return f.RunTimeout
	case "slack.exclude":
		return strings.Join(f.Slack.Exclude, ", ")
	case "slack.calls_per_minute":
		return intText(f.Slack.CallsPerMinute)
	case "slack.bots":
		return strings.Join(f.Slack.Bots, ", ")
	case "github.repos":
		return strings.Join(f.GitHub.Repos, ", ")
	case "sessions.groups":
		return strings.Join(f.Sessions.Groups, ", ")
	case "mute_days":
		return intText(f.MuteDays)
	case "retention":
		return f.Retention
	case "action_ttl":
		return f.ActionTTL
	}
	return ""
}

func parseFeedBool(v string) (bool, error) {
	switch strings.ToLower(v) {
	case "true", "on", "yes", "1":
		return true, nil
	case "false", "off", "no", "0":
		return false, nil
	}
	return false, fmt.Errorf("enabled %q: want true or false", v)
}

func parseFeedInt(key, v string, lo int) (*int, error) {
	if v == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo {
		return nil, fmt.Errorf("%s %q: want a whole number from %d", key, v, lo)
	}
	return &n, nil
}

// parseFeedSettingDuration reads a Go duration, "<n>d" or "0", as the
// config accessors do.
func parseFeedSettingDuration(v string) (time.Duration, bool) {
	if v == "0" {
		return 0, true
	}
	if n, ok := strings.CutSuffix(v, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil || days < 0 {
			return 0, false
		}
		return time.Duration(days) * 24 * time.Hour, true
	}
	d, err := time.ParseDuration(v)
	return d, err == nil
}

func splitFeedList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// feedSettingsView is Admin Menu → Feed: one tab per remote for its status,
// editing the laptop config's feed block that every remote is pushed.
type feedSettingsView struct {
	tab     int
	cursor  int
	editing bool
	input   textinput.Model
	err     string
	notice  string
}

// enterFeedSettings opens the page and reads the first remote's status.
func (m *Model) enterFeedSettings() tea.Cmd {
	m.feedSettings = &feedSettingsView{}
	m.state = viewStateFeedSettings
	return m.feedSettingsRead()
}

func (m *Model) feedSettingsRemotes() []config.Remote {
	return config.UniqueRemotes(agentAPIRemotes(m.cfg))
}

func (m *Model) feedSettingsRead() tea.Cmd {
	remotes := m.feedSettingsRemotes()
	v := m.feedSettings
	if v == nil || len(remotes) == 0 {
		return nil
	}
	v.tab = min(v.tab, len(remotes)-1)
	return m.feedReadCmd(remotes[v.tab].Host, true)
}

func (m *Model) handleFeedSettingsUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyPressMsg)
	v := m.feedSettings
	if v == nil {
		return m, nil
	}
	if !ok {
		if v.editing {
			var cmd tea.Cmd
			v.input, cmd = v.input.Update(msg)
			return m, cmd
		}
		return m, nil
	}
	if v.editing {
		switch km.String() {
		case "esc":
			v.editing = false
			return m, nil
		case "enter":
			v.editing = false
			m.saveFeedSetting(feedSettings[v.cursor].key, v.input.Value())
			return m, nil
		}
		var cmd tea.Cmd
		v.input, cmd = v.input.Update(km)
		return m, cmd
	}
	n := len(m.feedSettingsRemotes())
	switch km.String() {
	case "esc", "q":
		m.state = viewStateMenu
		return m, nil
	case "tab":
		if n > 0 {
			v.tab = (v.tab + 1) % n
		}
		return m, m.feedSettingsRead()
	case "shift+tab":
		if n > 0 {
			v.tab = (v.tab + n - 1) % n
		}
		return m, m.feedSettingsRead()
	case "j", "down":
		if v.cursor < len(feedSettings)-1 {
			v.cursor++
		}
	case "k", "up":
		if v.cursor > 0 {
			v.cursor--
		}
	case "r":
		return m, m.feedSettingsRead()
	case "space", " ":
		if feedSettings[v.cursor].key == "enabled" && m.cfg != nil {
			next := "true"
			if m.cfg.Feed.EnabledOrDefault() {
				next = "false"
			}
			m.saveFeedSetting("enabled", next)
			return m, nil
		}
	case "enter", "e":
		if m.cfg == nil {
			return m, nil
		}
		in := newTodoInput()
		in.SetValue(feedSettingValue(m.cfg.Feed, feedSettings[v.cursor].key))
		in.CursorEnd()
		in.Focus()
		v.input, v.editing, v.err = in, true, ""
		return m, textinput.Blink
	}
	return m, nil
}

// saveFeedSetting applies one value and writes the laptop config.
func (m *Model) saveFeedSetting(key, value string) {
	v := m.feedSettings
	if m.cfg == nil || v == nil {
		return
	}
	next := m.cfg.Feed
	if err := applyFeedSetting(&next, key, value); err != nil {
		v.err = err.Error()
		return
	}
	prev := m.cfg.Feed
	m.cfg.Feed = next
	if err := m.cfg.Save(); err != nil {
		m.cfg.Feed = prev
		v.err = "save: " + err.Error()
		return
	}
	v.err = ""
	v.notice = "saved " + key + "; press c on the Agent API page to push it to the remote"
}

// feedProjectedRuns is the expected runs per day: today's rate so far,
// bounded by the interval and the daily cap.
func feedProjectedRuns(f config.FeedConfig, batchesToday int, now time.Time) (projected, ceiling int) {
	ceiling = int(24 * time.Hour / f.IntervalOrDefault())
	if c := f.DailyBatchesOrDefault(); c > 0 && c < ceiling {
		ceiling = c
	}
	y, mo, d := now.Date()
	elapsed := now.Sub(time.Date(y, mo, d, 0, 0, 0, 0, now.Location()))
	if elapsed < time.Hour {
		elapsed = time.Hour
	}
	projected = int(float64(batchesToday) * float64(24*time.Hour) / float64(elapsed))
	return min(projected, ceiling), ceiling
}

func (m *Model) renderFeedSettings() string {
	v := m.feedSettings
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	var b strings.Builder
	b.WriteString("\n  " + title.Render("Feed settings") + "\n\n")
	remotes := m.feedSettingsRemotes()
	if v == nil || m.cfg == nil {
		return docStyle.Render(b.String())
	}
	var tabs []string
	for i, r := range remotes {
		name := remoteNameForHost(m.cfg, r.Host)
		if i == v.tab {
			tabs = append(tabs, lipgloss.NewStyle().Bold(true).Render("["+name+"]"))
		} else {
			tabs = append(tabs, dim.Render(name))
		}
	}
	if len(tabs) > 0 {
		b.WriteString("  " + strings.Join(tabs, "  ") + "\n")
		host := remotes[min(v.tab, len(remotes)-1)].Host
		b.WriteString("  " + m.feedSettingsStatus(host) + "\n\n")
	} else {
		b.WriteString("  " + dim.Render("No remotes configured.") + "\n\n")
	}
	f := m.cfg.Feed
	for i, s := range feedSettings {
		val := feedSettingValue(f, s.key)
		shown := val
		if shown == "" {
			shown = dim.Render(s.def + " (default)")
		}
		marker := "  "
		if i == v.cursor {
			marker = "▸ "
		}
		line := fmt.Sprintf("%s%-24s ", marker, s.key)
		if v.editing && i == v.cursor {
			v.input.SetWidth(40)
			line += v.input.View()
		} else {
			line += shown
		}
		b.WriteString("  " + line + "\n")
		if i == v.cursor {
			b.WriteString("    " + dim.Render(s.meaning) + "\n")
		}
	}
	b.WriteString("\n  " + dim.Render("The feed block applies to every remote; the tabs show each remote's status.") + "\n")
	if v.err != "" {
		b.WriteString("  " + errStyle.Render(v.err) + "\n")
	}
	if v.notice != "" {
		b.WriteString("  " + dim.Render(v.notice) + "\n")
	}
	help := "j/k move · enter edit (empty is the default) · space toggle enabled · tab remote · r reread · esc back"
	if v.editing {
		help = "enter save · esc cancel"
	}
	b.WriteString("  " + dim.Render(help) + "\n")
	return docStyle.Render(b.String())
}

// feedSettingsStatus is the remote's feed state and the projected runs.
func (m *Model) feedSettingsStatus(host string) string {
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	st := m.feed[host]
	switch {
	case st == nil || (!st.read && st.err == ""):
		return dim.Render("reading feed.status…")
	case st.err != "" && !st.read:
		return dim.Render("feed.status: " + firstLineOf(st.err))
	case st.off:
		return dim.Render("feed off on this remote (or its serve has no feed)")
	}
	proj, ceil := feedProjectedRuns(m.cfg.Feed, st.status.BatchesToday, time.Now())
	return dim.Render(fmt.Sprintf("collector %s · %d batches today · projected ~%d runs/day (at most %d with these settings)",
		st.status.Collector.State, st.status.BatchesToday, proj, ceil))
}
