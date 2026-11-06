package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/limits"
	"github.com/bouwerp/aiman/internal/usecase"
)

// usageLimitsPollEvery is how often a host that is still refreshing is asked
// again while the $ view is open.
const usageLimitsPollEvery = 2 * time.Second

// limitRows renders readings as one line each, with their age. A reading that
// does not apply on this host and says nothing is dropped.
func limitRows(readings []limits.Reading, now time.Time) []string {
	rows := make([]string, 0, len(readings))
	for _, r := range readings {
		if r.State == limits.StateUnavailable && r.Reason == "" {
			continue
		}
		parts := make([]string, 0, len(r.Windows)+len(r.Balances)+3)
		for _, w := range r.Windows {
			parts = append(parts, limitWindowText(w, now))
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
		if age := limitAgeText(r.ObservedAt, now); age != "" {
			parts = append(parts, age)
		}
		head := strings.TrimSpace(r.Provider + " " + r.Plan)
		rows = append(rows, strings.TrimSpace(head+"  "+strings.Join(parts, ", ")))
	}
	return rows
}

func limitWindowText(w limits.Window, now time.Time) string {
	if w.Reset {
		return w.Label + " reset"
	}
	text := w.Label + " " + w.LeftText()
	switch {
	case !w.ResetsAt.IsZero():
		return text + " (resets in " + limitSpan(w.ResetsAt.Sub(now)) + ")"
	case w.ResetsText != "":
		return text + " (resets " + w.ResetsText + ")"
	}
	return text
}

// limitSpan is a duration at the coarsest useful unit.
func limitSpan(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	}
	return strconv.Itoa(int(d.Hours()/24)) + "d"
}

func limitAgeText(at, now time.Time) string {
	if at.IsZero() {
		return ""
	}
	if now.Sub(at) < time.Minute {
		return "now"
	}
	return limitSpan(now.Sub(at)) + " ago"
}

// usageLimitsMsg is one host's limits.get answer for the $ view.
type usageLimitsMsg struct {
	host string
	snap limits.Snapshot
	err  error
}

// fetchUsageLimitsCmd asks one host for its limits.
func fetchUsageLimitsCmd(cfg *config.Config, host string, refresh bool) tea.Cmd {
	return func() tea.Msg {
		snap, err := fetchUsageLimits(cfg, host, refresh)
		return usageLimitsMsg{host: host, snap: snap, err: err}
	}
}

func fetchUsageLimits(cfg *config.Config, host string, refresh bool) (limits.Snapshot, error) {
	remote, ok := remoteForHost(cfg, host)
	if !ok {
		return limits.Snapshot{}, errors.New("remote not found")
	}
	mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw, err := usecase.ServeRPC(ctx, mgr, "limits.get", map[string]any{"refresh": refresh})
	if err != nil {
		return limits.Snapshot{}, err
	}
	var snap limits.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return limits.Snapshot{}, fmt.Errorf("decode limits: %w", err)
	}
	return snap, nil
}

// fetchUsageLimitsAll asks every host once.
func fetchUsageLimitsAll(cfg *config.Config, hosts []usageHost, refresh bool) tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(hosts))
	for _, h := range hosts {
		cmds = append(cmds, fetchUsageLimitsCmd(cfg, h.host, refresh))
	}
	return tea.Batch(cmds...)
}

// applyUsageLimits stores a host's answer and returns the next poll, or nil.
// A poll is scheduled only while the answer says Refreshing and the $ view is
// open. An old serve shows nothing; any other error keeps the last rows.
func (m *Model) applyUsageLimits(msg usageLimitsMsg) tea.Cmd {
	for i := range m.usageView.hosts {
		h := &m.usageView.hosts[i]
		if h.host != msg.host {
			continue
		}
		switch {
		case errors.Is(msg.err, usecase.ErrRPCUnsupported):
			return nil
		case msg.err != nil:
			h.limitsErr = msg.err.Error()
			return nil
		}
		h.limits = msg.snap.Readings
		h.limitsErr = ""
		if !msg.snap.Refreshing || m.state != viewStateUsage {
			return nil
		}
		cfg, host := m.cfg, msg.host
		return tea.Tick(usageLimitsPollEvery, func(time.Time) tea.Msg {
			return fetchUsageLimitsCmd(cfg, host, false)()
		})
	}
	return nil
}
