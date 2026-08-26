package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/remotesvc"
	"github.com/bouwerp/aiman/internal/infra/ssh"
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
	// auto marks an operation nobody asked for — the client noticing a remote is
	// behind and updating it. Such an op must not move the user's view or raise
	// the error screen a keypress-initiated one does.
	auto bool
}

type serveConfigWriter interface {
	Execute(ctx context.Context, command string) (string, error)
	WriteFile(ctx context.Context, path string, content []byte) error
}

func writeServeConfig(ctx context.Context, cfg *config.Config, remote serveConfigWriter) error {
	body, err := cfg.MarshalServeConfig()
	if err != nil {
		return fmt.Errorf("serializing Agent API settings: %w", err)
	}
	if _, err := remote.Execute(ctx, `install -d -m 700 "$HOME/.aiman" && (umask 077; touch "$HOME/.aiman/config.yaml") && chmod 600 "$HOME/.aiman/config.yaml"`); err != nil {
		return fmt.Errorf("creating remote Agent API config directory: %w", err)
	}
	if err := remote.WriteFile(ctx, ".aiman/config.yaml", body); err != nil {
		return fmt.Errorf("writing remote Agent API settings: %w", err)
	}
	if _, err := remote.Execute(ctx, `chmod 600 "$HOME/.aiman/config.yaml"`); err != nil {
		return fmt.Errorf("protecting remote Agent API settings: %w", err)
	}
	return nil
}

func syncRemoteServeConfig(cfg *config.Config, host string) error {
	r, ok := remoteForHost(cfg, host)
	if !ok {
		return fmt.Errorf("remote config not found for %s", host)
	}
	mgr := ssh.NewManager(ssh.Config{Host: r.Host, User: r.User, Root: r.Root})
	ctx, cancel := context.WithTimeout(context.Background(), remotesvc.OpTimeout)
	defer cancel()
	return writeServeConfig(ctx, cfg, mgr)
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

func remoteServiceOpCmd(cfg *config.Config, host string, kind remotesvc.Kind, op string, auto bool) tea.Cmd {
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
			return serviceOpMsg{host: host, kind: kind, op: op, auto: auto, err: fmt.Errorf("unknown op %s", op)}
		}
	}
	return func() tea.Msg {
		if kind == remotesvc.KindServe && op != "stop" {
			if err := syncRemoteServeConfig(cfg, host); err != nil {
				return serviceOpMsg{host: host, kind: kind, op: op, auto: auto, err: err}
			}
		}
		_, err := runRemoteScript(cfg, host, script)
		return serviceOpMsg{host: host, kind: kind, op: op, auto: auto, err: err}
	}
}

func (m *Model) applyDaemonProbe(msg daemonProbeMsg) (tea.Model, tea.Cmd) {
	d := msg.daemon
	if msg.err != nil && d.Status == "" {
		d.Status = domain.DaemonStatusError
	}
	if m.agentAPIProbing != nil {
		delete(m.agentAPIProbing, d.RemoteHost)
	}
	m.storeDaemon(d)
	m.applyRemoteFilter()
	// The probe carries the remote's version, so this is where a remote running
	// behind the client is noticed.
	autoUpdate := m.maybeAutoUpdateServe(d)
	if sel, ok := m.selectedDaemon(); ok && sel.RemoteHost == d.RemoteHost && sel.Kind == d.Kind {
		m.tmuxOutput = d.Logs
		if d.Logs == "" {
			m.tmuxOutput = "(no logs)"
		}
		m.viewport.SetContent(m.tmuxOutput)
	}
	return m, autoUpdate
}

func (m *Model) applyServiceOp(msg serviceOpMsg) (tea.Model, tea.Cmd) {
	if msg.auto {
		// Nobody asked for this, so it must leave the view exactly as it was:
		// no state change, and a failure reported rather than thrown up.
		if msg.err != nil {
			m.logPersistent("auto-%s %s on %s failed: %v", msg.op, msg.kind, msg.host, msg.err)
			return m, m.showToast("⚠️  could not update the agent API on "+msg.host, true, 8*time.Second)
		}
		return m, tea.Batch(
			m.showToast("✓  agent API updated on "+msg.host, false, 5*time.Second),
			probeRemoteServiceCmd(m.cfg, msg.host, msg.kind),
		)
	}
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
	return m, remoteServiceOpCmd(m.cfg, d.RemoteHost, kind, op, false), true
}
