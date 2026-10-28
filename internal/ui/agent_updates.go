package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/bouwerp/aiman/internal/agentupdate"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/usecase"
)

// agentUpdatesPollEvery is how often the agents page re-reads status while
// an update runs.
const agentUpdatesPollEvery = 2 * time.Second

// agentsRPCTimeout covers the SSH round trip plus serve's 25s refresh bound.
const agentsRPCTimeout = 45 * time.Second

// agentsRPC calls one agents.* method on a remote's agent API. Tests replace
// it.
var agentsRPC = func(ctx context.Context, r config.Remote, method string, params any) (json.RawMessage, error) {
	return usecase.ServeRPC(ctx, dialRemoteSSH(r), method, params)
}

// agentUpdatesView is the "Agents on <host>" page opened with a on the Agent
// API page. Rows come from agents.status on that host.
type agentUpdatesView struct {
	remote  config.Remote
	mode    string
	window  string
	rows    []agentupdate.AgentStatus
	cursor  int
	loading bool
	err     string
	notice  string
	// pollQueued is set while a poll tick is pending, so status answers
	// arriving close together never start a second tick chain.
	pollQueued bool
	// confirmMove names the agent whose move to the user npm prefix waits
	// on the confirm screen.
	confirmMove string
}

type agentsStatusMsg struct {
	host   string
	mode   string
	window string
	rows   []agentupdate.AgentStatus
	err    error
}

type agentsUpdateMsg struct {
	host    string
	results []agentupdate.Outcome
	err     error
}

type agentsPollMsg struct{ host string }

type agentsMoveMsg struct {
	host   string
	result agentupdate.Outcome
	err    error
}

func agentsMoveCmd(r config.Remote, agent string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), agentsRPCTimeout)
		defer cancel()
		raw, err := agentsRPC(ctx, r, "agents.move_prefix", map[string]any{"agent": agent})
		if err != nil {
			return agentsMoveMsg{host: r.Host, err: err}
		}
		var res struct {
			Result agentupdate.Outcome `json:"result"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return agentsMoveMsg{host: r.Host, err: fmt.Errorf("agents.move_prefix: %w", err)}
		}
		return agentsMoveMsg{host: r.Host, result: res.Result}
	}
}

// movePrefixLines are the exact commands the confirm screen shows.
func movePrefixLines(agent string) []string {
	e, ok := agentupdate.Lookup(agent)
	if !ok {
		return nil
	}
	var out []string
	for _, c := range agentupdate.MovePrefixCommands("~", e) {
		out = append(out, c.String())
	}
	return out
}

func agentsStatusCmd(r config.Remote, refresh bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), agentsRPCTimeout)
		defer cancel()
		raw, err := agentsRPC(ctx, r, "agents.status", map[string]any{"refresh": refresh})
		if err != nil {
			return agentsStatusMsg{host: r.Host, err: err}
		}
		var res struct {
			Mode   string                    `json:"mode"`
			Window string                    `json:"window"`
			Agents []agentupdate.AgentStatus `json:"agents"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return agentsStatusMsg{host: r.Host, err: fmt.Errorf("agents.status: %w", err)}
		}
		return agentsStatusMsg{host: r.Host, mode: res.Mode, window: res.Window, rows: res.Agents}
	}
}

func agentsUpdateCmd(r config.Remote, params map[string]any) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), agentsRPCTimeout)
		defer cancel()
		raw, err := agentsRPC(ctx, r, "agents.update", params)
		if err != nil {
			return agentsUpdateMsg{host: r.Host, err: err}
		}
		var res struct {
			Results []agentupdate.Outcome `json:"results"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return agentsUpdateMsg{host: r.Host, err: fmt.Errorf("agents.update: %w", err)}
		}
		return agentsUpdateMsg{host: r.Host, results: res.Results}
	}
}

func agentsPollCmd(host string) tea.Cmd {
	return tea.Tick(agentUpdatesPollEvery, func(time.Time) tea.Msg { return agentsPollMsg{host: host} })
}

// enterAgentUpdates opens the agents page for the remote selected on the
// Agent API page and reads its status.
func (m *Model) enterAgentUpdates() tea.Cmd {
	remotes := agentAPIRemotes(m.cfg)
	if len(remotes) == 0 {
		return nil
	}
	if m.agentAPICursor < 0 || m.agentAPICursor >= len(remotes) {
		m.agentAPICursor = 0
	}
	r := remotes[m.agentAPICursor]
	m.agentUpdates = &agentUpdatesView{remote: r, loading: true}
	m.state = viewStateAgentUpdates
	return agentsStatusCmd(r, false)
}

func (v *agentUpdatesView) selected() (agentupdate.AgentStatus, bool) {
	if v == nil || v.cursor < 0 || v.cursor >= len(v.rows) {
		return agentupdate.AgentStatus{}, false
	}
	return v.rows[v.cursor], true
}

func (v *agentUpdatesView) anyUpdating() bool {
	for _, r := range v.rows {
		if r.Status == agentupdate.StatusUpdating {
			return true
		}
	}
	return false
}

func (v *agentUpdatesView) anyOutdated() bool {
	for _, r := range v.rows {
		if r.Outdated() {
			return true
		}
	}
	return false
}

func (m *Model) handleAgentUpdatesUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyPressMsg)
	v := m.agentUpdates
	if !ok || v == nil {
		return m, nil
	}
	if v.confirmMove != "" {
		return m, v.handleMoveConfirm(km.String())
	}
	switch km.String() {
	case "esc", "q":
		m.state = viewStateAgentAPI
		return m, nil
	case "m":
		row, ok := v.selected()
		if !ok {
			return m, nil
		}
		if !agentupdate.CanMovePrefix(row) {
			v.notice = "m moves an npm install in a root-owned prefix to ~/.npm-global"
			return m, nil
		}
		v.notice = ""
		v.confirmMove = row.Agent
		return m, nil
	case "up", "k":
		if v.cursor > 0 {
			v.cursor--
		}
	case "down", "j":
		if v.cursor < len(v.rows)-1 {
			v.cursor++
		}
	case "r":
		v.loading = true
		v.notice = ""
		return m, agentsStatusCmd(v.remote, true)
	case "u":
		row, ok := v.selected()
		if !ok {
			return m, nil
		}
		if row.Installed == "" {
			v.notice = row.Agent + " is not installed"
			return m, nil
		}
		v.notice = "requesting " + row.Agent + " update…"
		return m, agentsUpdateCmd(v.remote, map[string]any{"agent": row.Agent})
	case "U":
		if !v.anyOutdated() {
			v.notice = "every installed agent is current"
			return m, nil
		}
		v.notice = "requesting updates for every outdated agent…"
		return m, agentsUpdateCmd(v.remote, map[string]any{"all": true})
	}
	return m, nil
}

// handleMoveConfirm answers the move confirm screen: y or enter runs
// agents.move_prefix, n or esc goes back to the list.
func (v *agentUpdatesView) handleMoveConfirm(key string) tea.Cmd {
	switch key {
	case "y", "enter":
		agent := v.confirmMove
		v.confirmMove = ""
		v.notice = "moving " + agent + " to ~/.npm-global…"
		return agentsMoveCmd(v.remote, agent)
	case "n", "esc", "q":
		v.confirmMove = ""
	}
	return nil
}

// routeAgentUpdatesMsg applies agents.* results on any view; answers for
// another host or a closed page are dropped. Polling continues only while
// the page is open and a row is updating.
func (m *Model) routeAgentUpdatesMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	v := m.agentUpdates
	switch msg := msg.(type) {
	case agentsStatusMsg:
		if v == nil || v.remote.Host != msg.host {
			return m, nil
		}
		v.loading = false
		if msg.err != nil {
			v.err = msg.err.Error()
			return m, nil
		}
		v.err = ""
		v.mode, v.window, v.rows = msg.mode, msg.window, msg.rows
		if v.cursor >= len(v.rows) {
			v.cursor = 0
		}
		return m, m.maybePollAgents()
	case agentsPollMsg:
		if v == nil || v.remote.Host != msg.host {
			return m, nil
		}
		v.pollQueued = false
		if m.state != viewStateAgentUpdates || !v.anyUpdating() {
			return m, nil
		}
		return m, agentsStatusCmd(v.remote, false)
	case agentsUpdateMsg:
		if v == nil || v.remote.Host != msg.host {
			return m, nil
		}
		if msg.err != nil {
			v.notice = ""
			v.err = msg.err.Error()
			return m, nil
		}
		v.err = ""
		v.notice = agentsUpdateNotice(msg.results)
		return m, agentsStatusCmd(v.remote, false)
	case agentsMoveMsg:
		if v == nil || v.remote.Host != msg.host {
			return m, nil
		}
		if msg.err != nil {
			v.notice = ""
			v.err = msg.err.Error()
			return m, nil
		}
		v.err = ""
		if msg.result.Accepted {
			v.notice = "moving " + msg.result.Agent + " to ~/.npm-global"
		} else {
			v.notice = msg.result.Agent + ": " + msg.result.Reason
		}
		return m, agentsStatusCmd(v.remote, false)
	}
	return m, nil
}

func (m *Model) maybePollAgents() tea.Cmd {
	v := m.agentUpdates
	if v == nil || v.pollQueued || m.state != viewStateAgentUpdates || !v.anyUpdating() {
		return nil
	}
	v.pollQueued = true
	return agentsPollCmd(v.remote.Host)
}

// agentsUpdateNotice summarises an agents.update answer in one line.
func agentsUpdateNotice(results []agentupdate.Outcome) string {
	if len(results) == 0 {
		return "nothing to update"
	}
	parts := make([]string, 0, len(results))
	for _, r := range results {
		if r.Accepted {
			parts = append(parts, "updating "+r.Agent)
		} else {
			parts = append(parts, r.Agent+": "+r.Reason)
		}
	}
	return strings.Join(parts, " · ")
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func agentStatusStyle(status string) lipgloss.Style {
	s := lipgloss.NewStyle()
	switch {
	case status == agentupdate.StatusCurrent:
		return s.Foreground(lipgloss.Color("2"))
	case status == agentupdate.StatusAvailable:
		return s.Foreground(lipgloss.Color("3")).Bold(true)
	case status == agentupdate.StatusUpdating:
		return s.Foreground(lipgloss.Color("6")).Bold(true)
	case status == agentupdate.StatusNeedsRoot, status == agentupdate.StatusFailed:
		return s.Foreground(lipgloss.Color("1")).Bold(true)
	case strings.HasPrefix(status, "deferred"):
		return s.Foreground(lipgloss.Color("3"))
	default:
		return s.Foreground(lipgloss.Color("240"))
	}
}

func (m *Model) renderAgentUpdatesView() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	selStyle := lipgloss.NewStyle().Background(lipgloss.Color("236"))

	v := m.agentUpdates
	if v == nil {
		return docStyle.Render("\n  No remote selected.\n")
	}
	host := v.remote.Host
	if v.remote.Name != "" {
		host = v.remote.Name
	}
	var b strings.Builder
	if v.confirmMove != "" {
		return docStyle.Render(renderMoveConfirm(v.confirmMove, host, titleStyle, dimStyle))
	}
	b.WriteString("\n  " + titleStyle.Render("Agents on "+host) + "\n\n")
	if v.mode != "" {
		line := "mode " + v.mode
		if v.mode == string(agentupdate.ModeAuto) && v.window != "" {
			line += " · window " + v.window
		}
		b.WriteString("  " + dimStyle.Render(line) + "\n\n")
	}

	b.WriteString(dimStyle.Render(fmt.Sprintf("  %-14s  %-12s  %-12s  %-11s  %s", "Agent", "Installed", "Latest", "Method", "Status")) + "\n")
	b.WriteString(dimStyle.Render("  "+strings.Repeat("─", 72)) + "\n")
	if len(v.rows) == 0 && v.loading {
		b.WriteString("  " + dimStyle.Render("Reading agent versions…") + "\n")
	}
	for i, r := range v.rows {
		status := r.Status
		if r.Held {
			status += " (held)"
		}
		line := fmt.Sprintf("  %-14s  %-12s  %-12s  %-11s  %s",
			truncatePad(r.Agent, 14), truncatePad(dashIfEmpty(r.Installed), 12), truncatePad(dashIfEmpty(r.Latest), 12),
			truncatePad(dashIfEmpty(string(r.Method)), 11), agentStatusStyle(r.Status).Render(status))
		if i == v.cursor {
			line = selStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}

	if row, ok := v.selected(); ok {
		if row.Path != "" {
			b.WriteString("\n  " + dimStyle.Render(row.Path) + "\n")
		}
		if row.Reason != "" && row.Reason != row.Status {
			b.WriteString("  " + errStyle.Render(row.Reason) + "\n")
		}
	}
	if v.loading && len(v.rows) > 0 {
		b.WriteString("\n  " + dimStyle.Render("Refreshing…") + "\n")
	}
	if v.notice != "" {
		b.WriteString("\n  " + v.notice + "\n")
	}
	if v.err != "" {
		b.WriteString("\n  " + errStyle.Render(v.err) + "\n")
	}
	keys := "u update selected  U update all outdated  r refresh  esc back"
	if row, ok := v.selected(); ok && agentupdate.CanMovePrefix(row) {
		keys = "u update selected  U update all outdated  m move to user prefix  r refresh  esc back"
	}
	b.WriteString("\n  " + dimStyle.Render(keys) + "\n")
	return docStyle.Render(b.String())
}

// renderMoveConfirm lists exactly what agents.move_prefix runs on host.
func renderMoveConfirm(agent, host string, titleStyle, dimStyle lipgloss.Style) string {
	var b strings.Builder
	b.WriteString("\n  " + titleStyle.Render("Move "+agent+" to the user npm prefix on "+host) + "\n\n")
	b.WriteString("  The install is in a root-owned npm prefix, so updating it needs root.\n")
	b.WriteString("  aiman serve runs, as you and never with sudo:\n\n")
	for _, line := range movePrefixLines(agent) {
		b.WriteString("    " + line + "\n")
	}
	b.WriteString("\n  ~/.npm-global/bin comes before the system paths in the PATH aiman uses on\n")
	b.WriteString("  this host (remote calls, agent launches, serve's update runner).\n")
	b.WriteString("  The root-owned copy stays installed; removing it needs root.\n")
	b.WriteString("  Running sessions keep the old copy until they restart.\n")
	b.WriteString("\n  " + dimStyle.Render("y run  n cancel") + "\n")
	return b.String()
}
