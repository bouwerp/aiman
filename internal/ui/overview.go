package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/usecase"
)

// overviewRPCTimeout bounds every overview call; serve answers overview.run
// at once and does the slow work in the background.
const overviewRPCTimeout = 30 * time.Second

// overviewRPC calls one overview.* method on a remote's agent API. Tests
// replace it.
var overviewRPC = func(ctx context.Context, r config.Remote, method string, params any) (json.RawMessage, error) {
	return usecase.ServeRPC(ctx, dialRemoteSSH(r), method, params)
}

// overviewView is the Overview page opened with O: the newest overview of
// one host's sessions, and r to start a run.
type overviewView struct {
	remote  config.Remote
	run     *server.OverviewRun
	loading bool
	// none is true when serve has no overview yet.
	none   bool
	err    string
	notice string
	scroll int
}

type overviewMsg struct {
	host string
	run  *server.OverviewRun
	// latest is the newest run of any state, without its text.
	latest *server.OverviewRun
	none   bool
	err    error
}

type overviewStartedMsg struct {
	host string
	run  *server.OverviewRun
	err  error
}

// overviewGetCmd reads overview.get, waiting for a foreground budget slot.
func (m *Model) overviewGetCmd(r config.Remote) tea.Cmd {
	budget := m.hostBudget
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), overviewRPCTimeout)
		defer cancel()
		release, err := budgetAcquire(ctx, budget, r.Host)
		if err != nil {
			return overviewMsg{host: r.Host, err: err}
		}
		raw, err := overviewRPC(ctx, r, "overview.get", map[string]any{})
		release(callTimedOut(err))
		return overviewResult(r.Host, raw, err)
	}
}

func overviewResult(host string, raw json.RawMessage, err error) overviewMsg {
	if err != nil {
		var rpcErr *usecase.RPCError
		if errors.As(err, &rpcErr) && rpcErr.Code == server.CodeNotFound {
			return overviewMsg{host: host, none: true}
		}
		return overviewMsg{host: host, err: err}
	}
	var res struct {
		Run    server.OverviewRun  `json:"run"`
		Latest *server.OverviewRun `json:"latest"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return overviewMsg{host: host, err: fmt.Errorf("overview.get: %w", err)}
	}
	return overviewMsg{host: host, run: &res.Run, latest: res.Latest}
}

func overviewRunCmd(r config.Remote) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), overviewRPCTimeout)
		defer cancel()
		raw, err := overviewRPC(ctx, r, "overview.run", map[string]any{})
		if err != nil {
			return overviewStartedMsg{host: r.Host, err: err}
		}
		var res struct {
			Run server.OverviewRun `json:"run"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return overviewStartedMsg{host: r.Host, err: fmt.Errorf("overview.run: %w", err)}
		}
		return overviewStartedMsg{host: r.Host, run: &res.Run}
	}
}

// overviewRemote is the host of the selected session, else the first remote
// with an agent API.
func (m *Model) overviewRemote() (config.Remote, bool) {
	if it, ok := m.selectedSessionItem(); ok {
		if r, ok := resolveRemote(m.cfg, it.session); ok {
			return r, true
		}
	}
	remotes := agentAPIRemotes(m.cfg)
	if len(remotes) == 0 {
		return config.Remote{}, false
	}
	return remotes[0], true
}

// enterOverview opens the Overview page and reads the newest overview.
func (m *Model) enterOverview() tea.Cmd {
	r, ok := m.overviewRemote()
	if !ok {
		return nil
	}
	m.overview = &overviewView{remote: r, loading: true}
	m.state = viewStateOverview
	return m.overviewGetCmd(r)
}

func (m *Model) handleOverviewUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyPressMsg)
	v := m.overview
	if !ok || v == nil {
		return m, nil
	}
	switch km.String() {
	case "esc", "q":
		m.state = viewStateMain
	case "up", "k":
		if v.scroll > 0 {
			v.scroll--
		}
	case "down", "j":
		v.scroll++
	case "r":
		if m.overviewBusy(v) {
			v.notice = "a run is already in progress"
			return m, nil
		}
		v.notice = "starting an overview run…"
		v.err = ""
		return m, overviewRunCmd(v.remote)
	case "R":
		v.loading = true
		return m, m.overviewGetCmd(v.remote)
	}
	return m, nil
}

// overviewBusy is true while the host's newest run is in progress.
func (m *Model) overviewBusy(v *overviewView) bool {
	if st := m.overviewStatus[v.remote.Host]; st != nil && st.read {
		return runInProgress(st.latest)
	}
	return runInProgress(v.run)
}

// routeOverviewMsg applies overview results on any view.
func (m *Model) routeOverviewMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	v := m.overview
	switch msg := msg.(type) {
	case overviewStatusMsg:
		return m, m.applyOverviewStatus(msg)
	case overviewMsg:
		drain := m.drainPending()
		if msg.err == nil {
			m.noteLatest(msg.host, msg.latest, msg.run)
		}
		if v == nil || v.remote.Host != msg.host {
			return m, drain
		}
		v.loading = false
		if msg.err != nil {
			v.err = msg.err.Error()
			return m, drain
		}
		v.err = ""
		v.none = msg.none
		if msg.run != nil {
			if v.run == nil || v.run.ID != msg.run.ID {
				v.scroll = 0
			}
			v.run = msg.run
			if !runInProgress(msg.run) {
				v.notice = ""
			}
		}
		return m, drain
	case overviewStartedMsg:
		if v == nil || v.remote.Host != msg.host {
			return m, nil
		}
		if msg.err != nil {
			v.notice = ""
			v.err = msg.err.Error()
			return m, nil
		}
		v.none = false
		v.run = msg.run
		v.scroll = 0
		v.notice = "running; the page follows its steps"
		m.noteLatest(msg.host, msg.run, nil)
		return m, nil
	}
	return m, nil
}

// noteLatest records the newest run a full read or a start brought, for the
// banner, when the summary has not seen it yet.
func (m *Model) noteLatest(host string, latest, run *server.OverviewRun) {
	if latest == nil {
		latest = run
	}
	if latest == nil {
		return
	}
	if m.overviewStatus == nil {
		m.overviewStatus = map[string]*overviewHostStatus{}
	}
	st := m.overviewStatus[host]
	if st == nil {
		st = &overviewHostStatus{}
		m.overviewStatus[host] = st
	}
	c := *latest
	c.Markdown, c.Report = "", nil
	if c.State == server.OverviewDone && c.NeedsYou == nil && latest.Report != nil {
		n := len(latest.Report.NeedsYou)
		c.NeedsYou = &n
	}
	st.latest, st.read, st.readAt = &c, true, time.Now()
}

// handleHostPageUpdate and renderHostPage serve the per-host pages opened
// from the dashboard: Agents on <host>, Overview, Feed and Feed settings.
func (m *Model) handleHostPageUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.state == viewStateSelfUpdate {
		if km, ok := msg.(tea.KeyPressMsg); ok {
			return m.handleSelfUpdateKey(km)
		}
		return m, nil
	}
	switch m.state {
	case viewStateOverview:
		return m.handleOverviewUpdate(msg)
	case viewStateFeed:
		return m.handleFeedPageUpdate(msg)
	case viewStateFeedSettings:
		return m.handleFeedSettingsUpdate(msg)
	}
	return m.handleAgentUpdatesUpdate(msg)
}

func (m *Model) renderHostPage() string {
	if m.state == viewStateSelfUpdate {
		return m.renderSelfUpdateView()
	}
	switch m.state {
	case viewStateOverview:
		return m.renderOverviewView()
	case viewStateFeed:
		return m.renderFeedPage()
	case viewStateFeedSettings:
		return m.renderFeedSettings()
	}
	return m.renderAgentUpdatesView()
}

func (m *Model) renderOverviewView() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	headStyle := lipgloss.NewStyle().Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	v := m.overview
	if v == nil {
		return docStyle.Render("\n  No remote selected.\n")
	}
	host := v.remote.Host
	if v.remote.Name != "" {
		host = v.remote.Name
	}
	width := m.width - 8
	if width < 20 {
		width = 72
	}
	var lines []string
	switch {
	case v.run == nil && v.loading:
		lines = append(lines, dimStyle.Render("Reading the overview…"))
	case v.run == nil && v.none:
		lines = append(lines, "No overview yet. Press r to run one.")
	case v.run != nil:
		lines = m.overviewLines(v, width, dimStyle, headStyle, errStyle)
	}
	if v.scroll > len(lines)-1 {
		v.scroll = max(0, len(lines)-1)
	}
	visible := lines[v.scroll:]
	if room := m.height - 9; room > 3 && len(visible) > room {
		visible = visible[:room]
	}
	var b strings.Builder
	b.WriteString("\n  " + titleStyle.Render("Overview of "+host) + "\n\n")
	for _, l := range visible {
		b.WriteString("  " + l + "\n")
	}
	b.WriteString("\n")
	if v.err != "" {
		b.WriteString("  " + errStyle.Render(v.err) + "\n")
	}
	if v.notice != "" {
		b.WriteString("  " + dimStyle.Render(v.notice) + "\n")
	}
	b.WriteString("  " + dimStyle.Render("r run · R reload · j/k scroll · esc back") + "\n")
	return docStyle.Render(b.String())
}

// overviewLines draws the newest run's header, steps and outcome, then the
// report: the run's own when it is done, else the older done run's with its
// time.
func (m *Model) overviewLines(v *overviewView, width int, dim, head, errStyle lipgloss.Style) []string {
	newest := v.run
	if st := m.overviewStatus[v.remote.Host]; st != nil && st.latest != nil {
		if l := st.latest; l.ID != v.run.ID || runInProgress(l) || l.State != v.run.State {
			// The summary is newer than the full read: its steps and state
			// are fresher, and a different run is the newest.
			c := *l
			if l.ID == v.run.ID {
				c.Markdown, c.Report = v.run.Markdown, v.run.Report
			}
			newest = &c
		}
	}
	r := newest
	meta := r.State + " · " + r.StartedAt.Local().Format("2006-01-02 15:04") + " · " + r.Agent
	if r.Sessions > 0 {
		meta += fmt.Sprintf(" · %d sessions", r.Sessions)
	}
	lines := []string{dim.Render(meta), ""}
	for _, s := range r.Steps {
		lines = append(lines, overviewStepLine(s, dim, errStyle))
	}
	if len(r.Steps) > 0 {
		lines = append(lines, "")
	}
	switch r.State {
	case server.OverviewDone:
		lines = append(lines, overviewReportLines(r.Markdown, width, head)...)
	case server.OverviewFailed:
		lines = append(lines, errStyle.Render("Failed: "+r.Error), "", "Press r to run again.")
		if o := v.run; o.ID != r.ID && o.State == server.OverviewDone {
			lines = append(lines, "", dim.Render("Last report, "+o.StartedAt.Local().Format("2006-01-02 15:04")+":"))
			lines = append(lines, overviewReportLines(o.Markdown, width, head)...)
		}
	}
	return lines
}

func overviewReportLines(markdown string, width int, head lipgloss.Style) []string {
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(markdown, "\n"), "\n") {
		if h, ok := strings.CutPrefix(l, "## "); ok {
			lines = append(lines, "", head.Render(h))
			continue
		}
		lines = append(lines, strings.Split(ansi.Wordwrap(l, width, " "), "\n")...)
	}
	return lines
}

// overviewStepLine is one step: ✓ label, duration and detail; ▸ label and
// time so far; ✗ label and its error; · label while pending.
func overviewStepLine(s server.OverviewStep, dim, errStyle lipgloss.Style) string {
	switch s.State {
	case server.StepDone:
		line := "✓ " + s.Label
		if s.StartedAt != nil && s.EndedAt != nil {
			line += "  " + s.EndedAt.Sub(*s.StartedAt).Round(time.Second).String()
		}
		if s.Detail != "" {
			line += "  " + dim.Render(s.Detail)
		}
		return line
	case server.StepRunning:
		line := "▸ " + s.Label
		if s.StartedAt != nil {
			line += "  " + clockSince(*s.StartedAt)
		}
		if s.Detail != "" {
			line += "  " + dim.Render(s.Detail)
		}
		return line
	case server.StepFailed:
		return errStyle.Render("✗ " + s.Label + "  " + s.Error)
	}
	return dim.Render("· " + s.Label)
}
