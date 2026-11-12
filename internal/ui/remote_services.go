package ui

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bouwerp/aiman/internal/debuglog"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/remotesvc"
	"github.com/bouwerp/aiman/internal/infra/rtk"
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

func writeServeConfig(ctx context.Context, cfg *config.Config, host string, remote serveConfigWriter) error {
	raw, err := configWithSSOFacts(cfg, host).MarshalServeConfig(host)
	if err != nil {
		return fmt.Errorf("serializing Agent API settings: %w", err)
	}
	if _, err := remote.Execute(ctx, `install -d -m 700 "$HOME/.aiman" && (umask 077; touch "$HOME/.aiman/config.yaml") && chmod 600 "$HOME/.aiman/config.yaml"`); err != nil {
		return fmt.Errorf("creating remote Agent API config directory: %w", err)
	}
	if err := remote.WriteFile(ctx, ".aiman/config.yaml", raw); err != nil {
		return fmt.Errorf("writing remote Agent API settings: %w", err)
	}
	if _, err := remote.Execute(ctx, `chmod 600 "$HOME/.aiman/config.yaml"`); err != nil {
		return fmt.Errorf("protecting remote Agent API settings: %w", err)
	}
	return nil
}

// configWithSSOFacts returns cfg with host's synced delegations completed from
// the laptop's ~/.aws/config. It fills copies: cfg is the config the TUI saves,
// and a fact written back there would count as explicit and outlive a change in
// ~/.aws/config.
func configWithSSOFacts(cfg *config.Config, host string) *config.Config {
	if cfg == nil {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		logSSOFactsSkipped(host, err)
		return cfg
	}
	awsConfig := filepath.Join(home, ".aws", "config")

	out := *cfg
	out.Remotes = slices.Clone(cfg.Remotes)
	for i := range out.Remotes {
		r := &out.Remotes[i]
		if r.Host != host {
			continue
		}
		r.AWSDelegation = delegationWithSSOFacts(r.AWSDelegation, awsConfig, host)
		r.AWSDelegations = slices.Clone(r.AWSDelegations)
		for j, d := range r.AWSDelegations {
			r.AWSDelegations[j] = delegationWithSSOFacts(d, awsConfig, host)
		}
		break
	}
	return &out
}

// delegationWithSSOFacts copies d with its SSO facts filled. A delegation whose
// profile cannot be read still goes out, without the facts.
func delegationWithSSOFacts(d *config.AWSDelegation, awsConfig, host string) *config.AWSDelegation {
	if d == nil || !d.SyncCredentials {
		return d
	}
	profile, err := awsdelegation.ReadSSOProfile(awsConfig, d.LocalSourceProfile())
	if err != nil {
		logSSOFactsSkipped(host, err)
		return d
	}
	filled := *d
	config.FillSSOFacts(&filled, profile)
	return &filled
}

func logSSOFactsSkipped(host string, err error) {
	if debuglog.Enabled() {
		_ = debuglog.Write(fmt.Sprintf("serve config %s: no SSO facts: %v\n", host, err))
	}
}

func syncRemoteServeConfig(cfg *config.Config, host string) error {
	r, ok := remoteForHost(cfg, host)
	if !ok {
		return fmt.Errorf("remote config not found for %s", host)
	}
	mgr := ssh.NewManager(ssh.Config{Host: r.Host, User: r.User, Root: r.Root})
	ctx, cancel := context.WithTimeout(context.Background(), remotesvc.OpTimeout)
	defer cancel()
	return writeServeConfig(ctx, cfg, host, mgr)
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
	return execRemoteScript(mgr, ctx, remoteScriptPath(r.Host), script)
}

// remoteScriptPath is unique per run. WriteFile truncates in place and bash
// reads a script as it runs, so a shared path would let a probe rewrite an update
// that is still running.
func remoteScriptPath(host string) string {
	return fmt.Sprintf("/tmp/aiman-%s-%s.sh", strings.ReplaceAll(host, "/", "-"), rand.Text())
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

// serviceOpScript builds the script for op. An install takes this client's
// release. A manual update takes the latest release, so it cannot move a host
// backwards; an auto-update (auto) takes version, which is the release it decided
// to move to. Both lookups may reach GitHub, so this runs inside the command.
func serviceOpScript(ctx context.Context, kind remotesvc.Kind, op, version string, auto bool) (string, error) {
	switch op {
	case "install":
		tag, err := remotesvc.InstallTag(ctx, version)
		if err != nil {
			return "", err
		}
		return remotesvc.InstallEnableScript(kind, tag), nil
	case "start", "restart", "reload":
		return remotesvc.StartScript(kind), nil
	case "stop":
		return remotesvc.StopScript(kind), nil
	case "update":
		if auto {
			return remotesvc.UpdateScript(kind, version), nil
		}
		tag, err := remotesvc.LatestReleaseTag(ctx)
		if err != nil {
			return "", err
		}
		return remotesvc.UpdateScript(kind, tag), nil
	default:
		return "", fmt.Errorf("unknown op %s", op)
	}
}

func remoteServiceOpCmd(cfg *config.Config, host string, kind remotesvc.Kind, op, version string, auto bool) tea.Cmd {
	return func() tea.Msg {
		script, err := serviceOpScript(context.Background(), kind, op, version, auto)
		if err != nil {
			return serviceOpMsg{host: host, kind: kind, op: op, auto: auto, err: err}
		}
		if kind == remotesvc.KindServe && op != "stop" {
			if err := syncRemoteServeConfig(cfg, host); err != nil {
				return serviceOpMsg{host: host, kind: kind, op: op, auto: auto, err: err}
			}
		}
		_, err = runRemoteScript(cfg, host, script)
		return serviceOpMsg{host: host, kind: kind, op: op, auto: auto, err: err}
	}
}

// rtkInstallMsg is the result of rtkInstallCmd. report is parsed even when
// err is set, because a wrong binary exits non-zero.
type rtkInstallMsg struct {
	host   string
	report rtk.InstallReport
	err    error
}

// rtkInstallCmd installs RTK on host and registers its agent hooks. It runs
// under remotesvc.OpTimeout like the service ops.
func rtkInstallCmd(cfg *config.Config, host string) tea.Cmd {
	return func() tea.Msg {
		out, err := runRemoteScript(cfg, host, rtk.InstallScript())
		return rtkInstallMsg{host: host, report: rtk.ParseInstall(out), err: err}
	}
}

func (m *Model) applyRTKInstall(msg rtkInstallMsg) (tea.Model, tea.Cmd) {
	m.state = m.loadingNext
	reprobe := probeRemoteServiceCmd(m.cfg, msg.host, remotesvc.KindServe)
	if msg.report.WrongBinary {
		toast := `⚠️  another "rtk" is on PATH on ` + msg.host + " (Rust Type Kit?)"
		return m, tea.Batch(m.showToast(toast, true, 8*time.Second), reprobe)
	}
	if msg.err != nil {
		m.lastError = fmt.Sprintf("install RTK on %s: %v", msg.host, msg.err)
		m.state = viewStateError
		return m, nil
	}
	toast, failed := rtkInstallToast(msg.host, msg.report)
	return m, tea.Batch(m.showToast(toast, failed, 6*time.Second), reprobe)
}

// rtkInstallToast lists hooks in rtk.HookTargets order. Any failed hook
// makes the toast an error.
func rtkInstallToast(host string, r rtk.InstallReport) (string, bool) {
	var ok, failed []string
	for _, h := range rtk.HookTargets {
		registered, present := r.Hooks[h.Binary]
		switch {
		case !present:
		case registered:
			ok = append(ok, h.Binary)
		default:
			failed = append(failed, h.Binary)
		}
	}
	toast := "✓  RTK " + r.Version + " on " + host
	if len(ok) > 0 {
		toast += " · hooks: " + strings.Join(ok, ", ")
	}
	if len(failed) > 0 {
		toast += " · failed: " + strings.Join(failed, ", ")
	}
	return toast, len(failed) > 0
}

func (m *Model) runSelectedRTKInstall() (tea.Model, tea.Cmd) {
	d, ok := m.selectedAgentAPIDaemon()
	if !ok {
		return m, nil
	}
	m.loadingMsg = "Installing RTK on " + d.RemoteHost + "..."
	m.loadingNext = viewStateAgentAPI
	m.state = viewStateLoading
	return m, rtkInstallCmd(m.cfg, d.RemoteHost)
}

// routeRemoteServiceMsg handles probe, op, GitHub release and session
// resource results on every view: an op started on the Agent API page or the Daemons tab finishes after
// the user may have moved on, and the release check's timer must keep running.
func (m *Model) routeRemoteServiceMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case daemonProbeMsg:
		return m.applyDaemonProbe(msg)
	case serviceOpMsg:
		return m.applyServiceOp(msg)
	case rtkInstallMsg:
		return m.applyRTKInstall(msg)
	case agentsStatusMsg, agentsUpdateMsg, agentsPollMsg, agentsMoveMsg:
		return m.routeAgentUpdatesMsg(msg)
	case gatewayPairMsg, gatewayPairTickMsg:
		return m.routeGatewayPairMsg(msg)
	case githubReleaseMsg:
		return m, m.applyGitHubRelease(msg)
	case githubReleaseDueMsg:
		return m, checkGitHubRelease()
	case resourcesMsg, resourcesTickMsg:
		// A fetch can finish, and the poll must keep running, off the list.
		return m, m.applyResourcesMsg(msg)
	}
	return m, nil
}

func (m *Model) applyDaemonProbe(msg daemonProbeMsg) (tea.Model, tea.Cmd) {
	d := msg.daemon
	if msg.err != nil && d.Status == "" {
		d.Status = domain.DaemonStatusError
	}
	if m.agentAPIProbing != nil && (d.Kind == string(remotesvc.KindServe) || d.Kind == "") {
		delete(m.agentAPIProbing, d.RemoteHost)
	}
	m.storeDaemon(d)
	m.applyRemoteFilter()
	// The probe carries the remote's version, so this is where a remote running
	// behind the client is noticed.
	// The hint goes first so an auto-update's toast, set after it, is the one shown.
	hint := m.refreshReleaseHint()
	autoUpdate := tea.Batch(hint, m.maybeAutoUpdateServe(d))
	if sel, ok := m.selectedDaemon(); ok && sel.RemoteHost == d.RemoteHost && sel.Kind == d.Kind {
		m.paneOutput = d.Logs
		if d.Logs == "" {
			m.paneOutput = "(no logs)"
		}
		m.viewport.SetContent(m.paneOutput)
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
			m.probeAfterServiceOp(msg),
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
		m.probeAfterServiceOp(msg),
	)
}

// probeAfterServiceOp re-probes what the op changed. Updating serve changes the
// gateway too (remotesvc.UpdateScript).
func (m *Model) probeAfterServiceOp(msg serviceOpMsg) tea.Cmd {
	probe := probeRemoteServiceCmd(m.cfg, msg.host, msg.kind)
	if msg.kind == remotesvc.KindServe && msg.op == "update" {
		return tea.Batch(probe, probeRemoteServiceCmd(m.cfg, msg.host, remotesvc.KindGateway))
	}
	return probe
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
	return m, remoteServiceOpCmd(m.cfg, d.RemoteHost, kind, op, m.version, false), true
}
