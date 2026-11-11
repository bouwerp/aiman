package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/remotesvc"
	"github.com/bouwerp/aiman/internal/selfupdate"
	"github.com/bouwerp/aiman/internal/server"
)

// hostUpdateDeps are what newHostUpdates takes from serve. Busy is the gate's
// counters in order: long calls, AWS logins, starting handovers, agent updates.
type hostUpdateDeps struct {
	Fetcher selfupdate.Fetcher
	Busy    []func() int
	Logf    func(string, ...any)
	// ReadGateway reads the host's aiman-gateway; nil means readHostGateway.
	ReadGateway func(context.Context) (selfupdate.Gateway, error)
}

// releaseFetcher asks GitHub for the latest release through remotesvc.
type releaseFetcher struct{}

func (releaseFetcher) Latest(ctx context.Context, etag string) (selfupdate.Release, bool, error) {
	rel, changed, err := remotesvc.LatestRelease(ctx, etag)
	if err != nil || !changed {
		return selfupdate.Release{}, changed, err
	}
	return selfupdate.Release{Tag: rel.Tag, Ready: rel.Ready(), ETag: rel.ETag, PublishedAt: rel.PublishedAt}, true, nil
}

// newHostUpdates builds the manager that keeps this host on the latest
// release. version is the ldflags version, passed unchanged: a dev build never
// updates itself.
func newHostUpdates(cfg config.HostUpdateConfig, dir, version string, deps hostUpdateDeps) *selfupdate.Manager {
	logf := deps.Logf
	if logf == nil {
		logf = log.Printf
	}
	fetcher := deps.Fetcher
	if fetcher == nil {
		fetcher = releaseFetcher{}
	}
	readGateway := deps.ReadGateway
	if readGateway == nil {
		readGateway = readHostGateway
	}
	logPath := filepath.Join(dir, "logs", "host-update.log")
	resultPath := filepath.Join(dir, "host-update.result.json")
	return &selfupdate.Manager{
		Version:    version,
		Fetcher:    fetcher,
		Runner:     selfupdate.NewScriptRunner(filepath.Join(dir, "tmp"), logPath, resultPath),
		StatePath:  filepath.Join(dir, "host-update.json"),
		ResultPath: resultPath,
		LogPath:    logPath,
		Policy: selfupdate.Policy{
			Mode:       selfupdate.Mode(cfg.ModeOrDefault()),
			CheckEvery: cfg.CheckEveryOrDefault(),
			RetryAfter: selfupdate.DefaultRetryAfter,
		},
		Busy:        deps.Busy,
		Logf:        logf,
		ReadGateway: readGateway,
	}
}

// readHostGateway reads the gateway the update script installs:
// $HOME/.local/bin/aiman-gateway, or the one on PATH only when that file is
// missing. Version is "vX.Y.Z" when --version parses, else its raw first
// line, which the manager reads as unknown.
func readHostGateway(ctx context.Context) (selfupdate.Gateway, error) {
	bin, err := hostGatewayBinary()
	if err != nil || bin == "" {
		return selfupdate.Gateway{}, err
	}
	gw := selfupdate.Gateway{Installed: true, Running: gatewayRunning(ctx)}
	out, err := exec.CommandContext(ctx, bin, "--version").Output() //nolint:gosec // G204: the binary is the gateway install target
	if err != nil {
		return gw, fmt.Errorf("%s --version: %w", bin, err)
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	gw.Version = line
	if v, ok := remotesvc.ParseVersion(line); ok {
		gw.Version = v.String()
	}
	return gw, nil
}

// hostGatewayBinary returns the gateway's path, or "" when none is installed.
func hostGatewayBinary() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	bin := filepath.Join(home, ".local", "bin", remotesvc.KindGateway.Binary())
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if p, err := exec.LookPath(remotesvc.KindGateway.Binary()); err == nil {
		return p, nil
	}
	return "", nil
}

// gatewayRunning reports whether an aiman-gateway process runs; a pgrep that
// cannot run counts as not running.
func gatewayRunning(ctx context.Context) bool {
	return exec.CommandContext(ctx, "pgrep", "-f", remotesvc.GatewayProcPattern()).Run() == nil //nolint:gosec // G204: fixed pattern from remotesvc
}

// wireHostUpdateNotify emits host_update with an empty session id: once per
// newly available release and once per gateway drift in notify mode, and once
// per settled install or gateway repair.
func wireHostUpdateNotify(ctx context.Context, m *selfupdate.Manager, n agentNotifier, host string) {
	emit := func(text string) {
		if err := n.Emit(ctx, "", "", domain.NotifyHostUpdate, text); err != nil {
			log.Printf("host update: notify: %v", err)
		}
	}
	m.OnNewVersion = func(r selfupdate.Release) {
		emit(hostNewVersionText(r.Tag, host))
	}
	m.OnApplied = func(a selfupdate.Attempt) {
		emit(hostAppliedText(a, m.Version, host))
	}
	// The manager calls it once per gateway version and serve tag
	// (State.NotifiedGateway).
	m.OnGatewayBehind = func(from, to string) {
		emit(gatewayBehindText(from, to, host))
	}
}

// gatewayBehindText is the notification for a gateway older than serve on a
// host in notify mode.
func gatewayBehindText(from, to, host string) string {
	if from == "" {
		from = "unknown version"
	}
	return fmt.Sprintf("gateway %s behind serve %s on %s (aiman host update)", from, to, host)
}

// hostNewVersionText is the notification for a release this host has not
// installed because its mode is notify.
func hostNewVersionText(tag, host string) string {
	return fmt.Sprintf("aiman %s available on %s (aiman host update)", tag, host)
}

// hostAppliedText reports one settled install. running is the version of the
// serve that settled it: equal to the target, serve took the update.
func hostAppliedText(a selfupdate.Attempt, running, host string) string {
	if a.IsGateway() {
		return gatewayAppliedText(a, host)
	}
	switch {
	case a.Status == selfupdate.AttemptOK:
		return fmt.Sprintf("serve updated %s → %s on %s", a.From, a.To, host)
	case running == a.To:
		return fmt.Sprintf("serve update to %s on %s: gateway install failed", a.To, host)
	default:
		msg := a.Error
		if msg == "" {
			msg = "install failed"
		}
		return fmt.Sprintf("serve update to %s on %s failed: %s", a.To, host, msg)
	}
}

// gatewayAppliedText reports one settled gateway repair.
func gatewayAppliedText(a selfupdate.Attempt, host string) string {
	if a.Status == selfupdate.AttemptOK {
		from := a.From
		if from == "" {
			from = "unknown version"
		}
		return fmt.Sprintf("gateway updated %s → %s on %s", from, a.To, host)
	}
	msg := a.Error
	if msg == "" {
		msg = "install failed"
	}
	return fmt.Sprintf("gateway update failed: %s (to %s on %s)", msg, a.To, host)
}

// startHostUpdates wires the manager into serve and starts its loops unless
// updates are off.
func startHostUpdates(ctx context.Context, srv *server.Server, m *selfupdate.Manager, host string) {
	wireHostUpdateNotify(ctx, m, srv.NotifyRecorder(), host)
	srv.SetHostUpdates(m, host)
	if m.Policy.Mode == selfupdate.ModeOff {
		return
	}
	go m.Finish(ctx)
	go m.Run(ctx)
}

const hostUsage = "Usage: aiman host stats [--json] | version [--json] | update [--json] | dump\n"

// runHostUpdateCmd is `aiman host version` and `aiman host update`.
func runHostUpdateCmd(args []string, out io.Writer) error {
	method := "host." + args[0]
	asJSON := false
	for _, a := range args[1:] {
		if a != "--json" {
			fmt.Fprint(os.Stderr, hostUsage)
			return errUsage
		}
		asJSON = true
	}
	sock, err := socketPath()
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return err
	}
	resp, err := server.Call(sock, method, map[string]any{})
	if err != nil {
		code := server.CodeServerNotRunning
		if !errors.Is(err, server.ErrServerNotRunning) {
			code = server.CodeInvalidParams
		}
		writeCLIError(code, err.Error())
		return err
	}
	if resp.Error != nil {
		writeCLIError(resp.Error.Code, resp.Error.Message)
		return errors.New(resp.Error.Message)
	}
	if asJSON {
		return writeJSON(resp.Result)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		return err
	}
	if method == "host.version" {
		var v server.HostVersionResult
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		_, err := io.WriteString(out, formatHostVersion(v))
		return err
	}
	var u server.HostUpdateResult
	if err := json.Unmarshal(raw, &u); err != nil {
		return err
	}
	_, err = io.WriteString(out, formatHostUpdate(u))
	return err
}

// formatHostVersion prints host.version as one status line and, when there is
// one, the last attempt.
func formatHostVersion(v server.HostVersionResult) string {
	latest := "latest -"
	if v.Latest != "" {
		ready := "not ready"
		if v.LatestReady {
			ready = "ready"
		}
		latest = fmt.Sprintf("latest %s (%s)", v.Latest, ready)
	}
	checked := "never checked"
	if v.CheckedAt != "" {
		checked = "checked " + localTime(v.CheckedAt)
	}
	s := fmt.Sprintf("%s · %s · %s · %s · %s\n", dash(v.Version), latest, dash(v.Mode), dash(v.Status), checked)
	if g := v.Gateway; g != nil {
		s += "gateway " + formatHostGateway(*g) + "\n"
	}
	if a := v.Attempt; a != nil {
		if a.Kind == selfupdate.AttemptKindGateway {
			s += "gateway "
		}
		s += fmt.Sprintf("attempt %s → %s %s at %s", dash(a.From), dash(a.To), dash(a.Status), localTime(a.At))
		if a.Exit != 0 {
			s += fmt.Sprintf(" (exit %d)", a.Exit)
		}
		if a.Error != "" {
			s += ": " + a.Error
		}
		s += "\n"
	}
	return s
}

// formatHostGateway is the gateway's version, status and whether it runs.
func formatHostGateway(g server.HostGateway) string {
	if !g.Installed && g.Status == selfupdate.GatewayNotInstalled {
		return "not installed"
	}
	run := "stopped"
	if g.Running {
		run = "running"
	}
	return fmt.Sprintf("%s · %s · %s", dash(g.Version), dash(g.Status), run)
}

func formatHostUpdate(u server.HostUpdateResult) string {
	if u.Accepted && u.Reason == selfupdate.ReasonGateway {
		return "accepted: gateway " + u.Target + "\n"
	}
	if u.Accepted {
		return "accepted: " + u.Target + "\n"
	}
	return "refused: " + u.Reason + "\n"
}

// localTime shows an RFC3339 time in the local zone, or the input when it
// does not parse.
func localTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return dash(s)
	}
	return t.Local().Format("2006-01-02 15:04")
}
