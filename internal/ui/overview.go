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

const (
	// overviewPollEvery is how often the open page re-reads a running run.
	overviewPollEvery = 5 * time.Second
	// overviewRPCTimeout covers the SSH round trip and serve building the
	// bundle and starting the agent.
	overviewRPCTimeout = 60 * time.Second
)

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
	none       bool
	err        string
	notice     string
	scroll     int
	pollQueued bool
}

type overviewMsg struct {
	host string
	run  *server.OverviewRun
	none bool
	err  error
}

type overviewStartedMsg struct {
	host string
	run  *server.OverviewRun
	err  error
}

type overviewPollMsg struct{ host string }

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
		Run server.OverviewRun `json:"run"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return overviewMsg{host: host, err: fmt.Errorf("overview.get: %w", err)}
	}
	return overviewMsg{host: host, run: &res.Run}
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

func overviewPollCmd(host string) tea.Cmd {
	return tea.Tick(overviewPollEvery, func(time.Time) tea.Msg { return overviewPollMsg{host: host} })
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
		if v.run != nil && (v.run.State == server.OverviewRunning || v.run.State == server.OverviewStarting) {
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

// routeOverviewMsg applies overview results on any view: the answer also
// sets the host's needs-you count shown in the header.
func (m *Model) routeOverviewMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	v := m.overview
	switch msg := msg.(type) {
	case overviewMsg:
		if msg.err == nil {
			m.setOverviewNeeds(msg.host, msg.run)
		}
		if v == nil || v.remote.Host != msg.host {
			return m, nil
		}
		v.loading = false
		if msg.err != nil {
			v.err = msg.err.Error()
			return m, nil
		}
		v.err = ""
		v.none = msg.none
		if msg.run != nil {
			if v.run == nil || v.run.ID != msg.run.ID {
				v.scroll = 0
			}
			v.run = msg.run
			if msg.run.State == server.OverviewDone || msg.run.State == server.OverviewFailed {
				v.notice = ""
			}
		}
		return m, m.maybePollOverview()
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
		v.notice = "running; the page updates when it finishes"
		return m, m.maybePollOverview()
	case overviewPollMsg:
		if v == nil || v.remote.Host != msg.host {
			return m, nil
		}
		v.pollQueued = false
		if m.state != viewStateOverview || !v.inProgress() {
			return m, nil
		}
		return m, m.overviewGetCmd(v.remote)
	}
	return m, nil
}

func (v *overviewView) inProgress() bool {
	return v.run != nil && (v.run.State == server.OverviewRunning || v.run.State == server.OverviewStarting)
}

func (m *Model) maybePollOverview() tea.Cmd {
	v := m.overview
	if v == nil || v.pollQueued || m.state != viewStateOverview || !v.inProgress() {
		return nil
	}
	v.pollQueued = true
	return overviewPollCmd(v.remote.Host)
}

// setOverviewNeeds keeps the needs-you count of the newest done overview per
// host for the header.
func (m *Model) setOverviewNeeds(host string, run *server.OverviewRun) {
	if run == nil || run.State != server.OverviewDone || run.Report == nil {
		return
	}
	if m.overviewNeeds == nil {
		m.overviewNeeds = map[string]int{}
	}
	m.overviewNeeds[host] = len(run.Report.NeedsYou)
}

// overviewEventFetch rereads a host's overview when serve says a run
// changed, so the header count and an open page follow it.
func (m *Model) overviewEventFetch(host string) tea.Cmd {
	for _, r := range agentAPIRemotes(m.cfg) {
		if r.Host == host {
			release, ok := m.budgetTry(host)
			if !ok {
				return nil
			}
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), overviewRPCTimeout)
				defer cancel()
				raw, err := overviewRPC(ctx, r, "overview.get", map[string]any{})
				release(callTimedOut(err))
				return overviewResult(host, raw, err)
			}
		}
	}
	return nil
}

// overviewHeaderPart is "overview: n need you" for the gauge host, or "".
func (m *Model) overviewHeaderPart() string {
	n := m.overviewNeeds[m.gaugeHost()]
	if n == 0 {
		return ""
	}
	return a1Text(fmt.Sprintf("overview: %d need you", n), resourceColor(true), false, false)
}

// handleHostPageUpdate and renderHostPage serve the per-host pages opened
// from the dashboard: Agents on <host> and Overview.
func (m *Model) handleHostPageUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.state == viewStateOverview {
		return m.handleOverviewUpdate(msg)
	}
	return m.handleAgentUpdatesUpdate(msg)
}

func (m *Model) renderHostPage() string {
	if m.state == viewStateOverview {
		return m.renderOverviewView()
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
		r := v.run
		meta := r.State + " · " + r.StartedAt.Local().Format("2006-01-02 15:04") + " · " + r.Agent
		if r.Sessions > 0 {
			meta += fmt.Sprintf(" · %d sessions", r.Sessions)
		}
		lines = append(lines, dimStyle.Render(meta), "")
		switch r.State {
		case server.OverviewDone:
			for _, l := range strings.Split(strings.TrimRight(r.Markdown, "\n"), "\n") {
				if h, ok := strings.CutPrefix(l, "## "); ok {
					lines = append(lines, "", headStyle.Render(h))
					continue
				}
				lines = append(lines, strings.Split(ansi.Wordwrap(l, width, " "), "\n")...)
			}
		case server.OverviewFailed:
			lines = append(lines, errStyle.Render("Failed: "+r.Error), "", "Press r to run again.")
		default:
			lines = append(lines, "Running for "+time.Since(r.StartedAt).Round(time.Second).String()+"…")
		}
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
