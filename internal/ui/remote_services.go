package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/remotesvc"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	tea "github.com/charmbracelet/bubbletea"
)

type daemonProbeMsg struct {
	daemon domain.Daemon
	err    error
}

type serviceOpMsg struct {
	host string
	kind remotesvc.Kind
	op   string
	err  error
}

func (m *Model) selectedDaemon() (domain.Daemon, bool) {
	if m.state == viewStateAgentAPI {
		return m.selectedAgentAPIDaemon()
	}
	sel := m.daemonList.SelectedItem()
	di, ok := sel.(daemonItem)
	if !ok {
		return domain.Daemon{}, false
	}
	return di.daemon, true
}

func (m *Model) storeDaemon(d domain.Daemon) {
	if m.daemons == nil {
		m.daemons = map[string]domain.Daemon{}
	}
	if d.Kind == "" {
		d.Kind = string(remotesvc.KindTrigger)
	}
	m.daemons[domain.DaemonKey(d.RemoteHost, d.Kind)] = d
}

func remoteForHost(cfg *config.Config, host string) (config.Remote, bool) {
	if cfg == nil {
		return config.Remote{}, false
	}
	for _, r := range cfg.Remotes {
		if r.Host == host {
			return r, true
		}
	}
	return config.Remote{}, false
}

func execRemoteScript(mgr *ssh.Manager, ctx context.Context, path, script string) (string, error) {
	if err := mgr.WriteFile(ctx, path, []byte("#!/bin/bash\n"+script+"\n")); err != nil {
		return "", err
	}
	return mgr.ExecuteWithTimeout(ctx, "bash "+path+"; rm -f "+path, remotesvc.OpTimeout)
}

func runRemoteScript(cfg *config.Config, host, script string) (string, error) {
	r, ok := remoteForHost(cfg, host)
	if !ok {
		return "", fmt.Errorf("remote config not found for %s", host)
	}
	mgr := ssh.NewManager(ssh.Config{Host: r.Host, User: r.User, Root: r.Root})
	ctx, cancel := context.WithTimeout(context.Background(), remotesvc.OpTimeout)
	defer cancel()
	path := fmt.Sprintf("/tmp/aiman-%s.sh", strings.ReplaceAll(r.Host, "/", "-"))
	return execRemoteScript(mgr, ctx, path, script)
}

func probeRemoteServiceCmd(cfg *config.Config, host string, kind remotesvc.Kind) tea.Cmd {
	return func() tea.Msg {
		out, err := runRemoteScript(cfg, host, remotesvc.ProbeScript(kind))
		if err != nil {
			return daemonProbeMsg{daemon: domain.Daemon{RemoteHost: host, Kind: string(kind), Status: domain.DaemonStatusError, Logs: err.Error()}, err: err}
		}
		return daemonProbeMsg{daemon: remotesvc.ParseProbe(kind, host, out)}
	}
}

func remoteServiceOpCmd(cfg *config.Config, host string, kind remotesvc.Kind, op string) tea.Cmd {
	var script string
	switch op {
	case "install":
		script = remotesvc.InstallEnableScript(kind)
	case "start", "restart", "reload":
		script = remotesvc.StartScript(kind)
	case "stop":
		script = remotesvc.StopScript(kind)
	case "update":
		script = remotesvc.UpdateScript(kind)
	default:
		return func() tea.Msg {
			return serviceOpMsg{host: host, kind: kind, op: op, err: fmt.Errorf("unknown op %s", op)}
		}
	}
	return func() tea.Msg {
		_, err := runRemoteScript(cfg, host, script)
		return serviceOpMsg{host: host, kind: kind, op: op, err: err}
	}
}

func (m *Model) applyDaemonProbe(msg daemonProbeMsg) (tea.Model, tea.Cmd) {
	d := msg.daemon
	if msg.err != nil && d.Status == "" {
		d.Status = domain.DaemonStatusError
	}
	m.storeDaemon(d)
	m.applyRemoteFilter()
	if sel, ok := m.selectedDaemon(); ok && sel.RemoteHost == d.RemoteHost && sel.Kind == d.Kind {
		m.tmuxOutput = d.Logs
		if d.Logs == "" {
			m.tmuxOutput = "(no logs)"
		}
		m.viewport.SetContent(m.tmuxOutput)
	}
	return m, nil
}

func (m *Model) applyServiceOp(msg serviceOpMsg) (tea.Model, tea.Cmd) {
	m.state = m.loadingNext
	if msg.err != nil {
		m.lastError = fmt.Sprintf("%s %s on %s: %v", msg.op, msg.kind, msg.host, msg.err)
		m.state = viewStateError
		return m, nil
	}
	toast := fmt.Sprintf("%s %s on %s", msg.op, msg.kind, msg.host)
	return m, tea.Batch(
		m.showToast("✓  "+toast, false, 4*time.Second),
		probeRemoteServiceCmd(m.cfg, msg.host, msg.kind),
	)
}

func (m *Model) runSelectedServiceOp(op, loading string) (tea.Model, tea.Cmd, bool) {
	d, ok := m.selectedDaemon()
	if !ok {
		return m, nil, true
	}
	kind := remotesvc.Kind(d.Kind)
	if kind == "" {
		kind = remotesvc.KindTrigger
	}
	m.loadingMsg = loading + " " + string(kind) + " on " + d.RemoteHost + "..."
	if m.state == viewStateAgentAPI {
		m.loadingNext = viewStateAgentAPI
	} else {
		m.loadingNext = viewStateMain
	}
	m.state = viewStateLoading
	return m, remoteServiceOpCmd(m.cfg, d.RemoteHost, kind, op), true
}
