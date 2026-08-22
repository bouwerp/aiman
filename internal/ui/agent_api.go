package ui

import (
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/remotesvc"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func (m *Model) enterAgentAPI() tea.Cmd {
	m.state = viewStateAgentAPI
	if m.agentAPICursor < 0 {
		m.agentAPICursor = 0
	}
	remotes := agentAPIRemotes(m.cfg)
	if m.agentAPICursor >= len(remotes) {
		m.agentAPICursor = 0
	}
	var cmds []tea.Cmd
	for _, r := range remotes {
		cmds = append(cmds, probeRemoteServiceCmd(m.cfg, r.Host, remotesvc.KindServe))
	}
	return tea.Batch(cmds...)
}

func agentAPIRemotes(cfg *config.Config) []config.Remote {
	if cfg == nil {
		return nil
	}
	return cfg.Remotes
}

func (m *Model) selectedAgentAPIDaemon() (domain.Daemon, bool) {
	remotes := agentAPIRemotes(m.cfg)
	if len(remotes) == 0 {
		return domain.Daemon{}, false
	}
	if m.agentAPICursor < 0 || m.agentAPICursor >= len(remotes) {
		m.agentAPICursor = 0
	}
	host := remotes[m.agentAPICursor].Host
	d, ok := m.daemons[domain.DaemonKey(host, string(remotesvc.KindServe))]
	if !ok {
		d = domain.Daemon{RemoteHost: host, Kind: string(remotesvc.KindServe), Status: domain.DaemonStatusStopped}
	}
	if d.Kind == "" {
		d.Kind = string(remotesvc.KindServe)
	}
	return d, true
}

func (m *Model) handleAgentAPIUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	remotes := agentAPIRemotes(m.cfg)
	switch km.String() {
	case "esc", "q":
		m.state = viewStateMenu
		return m, nil
	case "up", "k":
		if m.agentAPICursor > 0 {
			m.agentAPICursor--
		}
		return m, nil
	case "down", "j":
		if m.agentAPICursor < len(remotes)-1 {
			m.agentAPICursor++
		}
		return m, nil
	case "i":
		model, cmd, _ := m.runSelectedServiceOp("install", "Installing")
		return model, cmd
	case "s", "ctrl+r":
		model, cmd, _ := m.runSelectedServiceOp("restart", "Restarting")
		return model, cmd
	case "c":
		model, cmd, _ := m.runSelectedServiceOp("reload", "Reloading")
		return model, cmd
	case "u":
		model, cmd, _ := m.runSelectedServiceOp("update", "Updating")
		return model, cmd
	case "ctrl+k":
		model, cmd, _ := m.runSelectedServiceOp("stop", "Stopping")
		return model, cmd
	case "r":
		if d, ok := m.selectedAgentAPIDaemon(); ok {
			return m, probeRemoteServiceCmd(m.cfg, d.RemoteHost, remotesvc.KindServe)
		}
		return m, nil
	}
	return m, nil
}

func (m *Model) renderAgentAPIView() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	badStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	selStyle := lipgloss.NewStyle().Background(lipgloss.Color("236"))

	var b strings.Builder
	b.WriteString("\n  " + titleStyle.Render("Agent API") + "\n\n")
	b.WriteString("  In-pane agents talk to this process on each remote (skill / aiman session).\n")
	b.WriteString("  Select a remote, then install or manage it. This is not the laptop TUI.\n\n")

	remotes := agentAPIRemotes(m.cfg)
	if len(remotes) == 0 {
		b.WriteString(dimStyle.Render("  No remotes configured. Add one from Manage Remote Servers first.\n"))
		b.WriteString("\n  " + dimStyle.Render("esc back") + "\n")
		return docStyle.Render(b.String())
	}

	b.WriteString(dimStyle.Render(fmt.Sprintf("  %-12s  %-28s  %-10s  %-8s  %s", "Status", "Host", "Driver", "Socket", "Version")) + "\n")
	b.WriteString(dimStyle.Render("  "+strings.Repeat("─", 78)) + "\n")

	for i, r := range remotes {
		d := m.daemons[domain.DaemonKey(r.Host, string(remotesvc.KindServe))]
		status := string(d.Status)
		if status == "" {
			status = string(domain.DaemonStatusStopped)
		}
		statusCell := fmt.Sprintf("%-12s", status)
		switch d.Status {
		case domain.DaemonStatusRunning:
			statusCell = okStyle.Render(statusCell)
		case domain.DaemonStatusStopped, "":
			statusCell = badStyle.Render(fmt.Sprintf("%-12s", string(domain.DaemonStatusStopped)))
		default:
			statusCell = badStyle.Render(statusCell)
		}
		driver := d.Driver
		if driver == "" || driver == "none" {
			driver = "—"
		}
		sock := "—"
		if d.Kind == string(remotesvc.KindServe) || d.Kind == "" {
			if d.SocketOK {
				sock = okStyle.Render("up")
			} else {
				sock = badStyle.Render("down")
			}
		}
		ver := d.Version
		if ver == "" || ver == "missing" {
			ver = "—"
		}
		host := r.Host
		if r.Name != "" {
			host = r.Name + "  " + r.Host
		}
		line := fmt.Sprintf("  %s  %-28s  %-10s  %-8s  %s", statusCell, truncatePad(host, 28), truncatePad(driver, 10), sock, ver)
		if i == m.agentAPICursor {
			line = selStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}

	if d, ok := m.selectedAgentAPIDaemon(); ok {
		b.WriteString("\n")
		if d.Status != domain.DaemonStatusRunning {
			b.WriteString("  Press i to install and enable. Agents cannot use the skill until this is RUNNING and the socket is up.\n")
		}
		if logs := strings.TrimSpace(d.Logs); logs != "" {
			b.WriteString("\n" + dimStyle.Render("  Logs") + "\n")
			for _, line := range tailLines(logs, 8) {
				b.WriteString(dimStyle.Render("  "+line) + "\n")
			}
		}
	}

	b.WriteString("\n  " + dimStyle.Render("i install/enable  s restart  c reload  u update  r probe  ctrl+k stop  esc back") + "\n")
	return docStyle.Render(b.String())
}

func truncatePad(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return fmt.Sprintf("%-*s", n, s)
}

func tailLines(s string, n int) []string {
	lines := strings.Split(s, "\n")
	if n < 1 || len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}
