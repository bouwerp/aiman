package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/usecase"
)

// overviewHostStatus is what the TUI knows of one host's newest overview run:
// the summary read, independent of the Overview page.
type overviewHostStatus struct {
	// latest is the newest run of any state, without its report text; nil
	// when the host has no run (or an older serve).
	latest *server.OverviewRun
	// read is true once a read answered, even with no run.
	read     bool
	readAt   time.Time
	inflight bool
}

func (s *overviewHostStatus) inProgress() bool {
	return s != nil && runInProgress(s.latest)
}

func runInProgress(r *server.OverviewRun) bool {
	return r != nil && (r.State == server.OverviewRunning || r.State == server.OverviewStarting)
}

// overviewStatusMsg is the answer to one overview.get {summary: true}.
type overviewStatusMsg struct {
	host   string
	latest *server.OverviewRun
	none   bool
	err    error
}

// overviewStatusCmd reads host's summary with a background slot. It returns
// nil when a read is in flight or no slot is free; the caller decides whether
// to remember that.
func (m *Model) overviewStatusCmd(host string) tea.Cmd {
	for _, r := range agentAPIRemotes(m.cfg) {
		if r.Host != host {
			continue
		}
		if m.overviewStatus == nil {
			m.overviewStatus = map[string]*overviewHostStatus{}
		}
		st := m.overviewStatus[host]
		if st == nil {
			st = &overviewHostStatus{}
			m.overviewStatus[host] = st
		}
		if st.inflight {
			return nil
		}
		release, ok := m.budgetTry(host)
		if !ok {
			return nil
		}
		st.inflight = true
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), overviewRPCTimeout)
			defer cancel()
			raw, err := overviewRPC(ctx, r, "overview.get", map[string]any{"summary": true})
			release(callTimedOut(err))
			return overviewStatusResult(host, raw, err)
		}
	}
	return nil
}

func overviewStatusResult(host string, raw json.RawMessage, err error) overviewStatusMsg {
	if err != nil {
		var rpcErr *usecase.RPCError
		// An older serve answers not_found, or an unknown-method
		// invalid_params: no run, no banner.
		if errors.As(err, &rpcErr) && (rpcErr.Code == server.CodeNotFound || rpcErr.Code == server.CodeInvalidParams) {
			return overviewStatusMsg{host: host, none: true}
		}
		return overviewStatusMsg{host: host, err: err}
	}
	var res struct {
		Latest *server.OverviewRun `json:"latest"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return overviewStatusMsg{host: host, err: fmt.Errorf("overview.get: %w", err)}
	}
	return overviewStatusMsg{host: host, latest: res.Latest, none: res.Latest == nil}
}

// fetchOverviewStatusAll reads the summary of every agent API host that has
// not answered yet or whose run is in progress (the 10s backup read). A host
// with no free slot is tried again on the next resources tick.
func (m *Model) fetchOverviewStatusAll() tea.Cmd {
	var cmds []tea.Cmd
	seen := map[string]bool{}
	for _, r := range agentAPIRemotes(m.cfg) {
		if seen[r.Host] {
			continue
		}
		seen[r.Host] = true
		if st := m.overviewStatus[r.Host]; st != nil && st.read && !st.inProgress() {
			continue
		}
		if cmd := m.overviewStatusCmd(r.Host); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

// overviewEventFetch rereads a host's summary when serve says a run changed.
// A read that finds no slot, or one already in flight, is remembered and run
// by drainPending.
func (m *Model) overviewEventFetch(host string) tea.Cmd {
	known := false
	for _, r := range agentAPIRemotes(m.cfg) {
		if r.Host == host {
			known = true
		}
	}
	if !known {
		return nil
	}
	cmd := m.overviewStatusCmd(host)
	if cmd == nil {
		m.markPending("overview.get", host, "")
	}
	return cmd
}

// applyOverviewStatus stores a summary answer. When it brings a newer run or
// a state change and the page is open on that host, the page reads the full
// run again.
func (m *Model) applyOverviewStatus(msg overviewStatusMsg) tea.Cmd {
	if m.overviewStatus == nil {
		m.overviewStatus = map[string]*overviewHostStatus{}
	}
	st := m.overviewStatus[msg.host]
	if st == nil {
		st = &overviewHostStatus{}
		m.overviewStatus[msg.host] = st
	}
	st.inflight = false
	cmds := []tea.Cmd{m.drainPending()}
	if msg.err != nil {
		return tea.Batch(cmds...)
	}
	old := st.latest
	st.latest = msg.latest
	st.read = true
	st.readAt = time.Now()
	changed := (old == nil) != (msg.latest == nil) ||
		(old != nil && msg.latest != nil && (old.ID != msg.latest.ID || old.State != msg.latest.State))
	if v := m.overview; changed && v != nil && m.state == viewStateOverview && v.remote.Host == msg.host {
		cmds = append(cmds, m.overviewGetCmd(v.remote))
	}
	return tea.Batch(cmds...)
}

// overviewBannerFor is the overview part of the header for host, or "".
func (m *Model) overviewBannerFor(host string) string {
	st := m.overviewStatus[host]
	if st == nil || st.latest == nil {
		return ""
	}
	r := st.latest
	switch r.State {
	case server.OverviewStarting, server.OverviewRunning:
		text := "overview ▸ " + currentStepLabel(r) + " " + clockSince(r.StartedAt)
		return a1Text(text, a1Dim, false, false)
	case server.OverviewDone:
		text := "overview " + overviewTime(runEnd(r))
		n := 0
		if r.NeedsYou != nil {
			n = *r.NeedsYou
		}
		if n > 0 {
			return a1Text(fmt.Sprintf("%s · %d need you", text, n), resourceColor(true), false, false)
		}
		return a1Text(text, a1Dim, false, false)
	case server.OverviewFailed:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render("overview failed " + overviewTime(runEnd(r)))
	}
	return ""
}

func runEnd(r *server.OverviewRun) time.Time {
	if r.EndedAt != nil {
		return *r.EndedAt
	}
	return r.StartedAt
}

// currentStepLabel is the label of the step in progress, else the first one
// not done.
func currentStepLabel(r *server.OverviewRun) string {
	for _, s := range r.Steps {
		if s.State == server.StepRunning {
			return s.Label
		}
	}
	for _, s := range r.Steps {
		if s.State == server.StepPending {
			return s.Label
		}
	}
	return r.State
}

// clockSince is m:ss since t.
func clockSince(t time.Time) string {
	d := max(time.Since(t), 0)
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

// overviewTime is HH:MM in local time, with the date when it is not today.
func overviewTime(t time.Time) string {
	t = t.Local()
	if t.YearDay() == time.Now().YearDay() && t.Year() == time.Now().Year() {
		return t.Format("15:04")
	}
	return t.Format("01-02 15:04")
}
