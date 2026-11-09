package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
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
		Busy: deps.Busy,
		Logf: logf,
	}
}

// wireHostUpdateNotify emits host_update with an empty session id: once per
// newly available release in notify mode, and once per settled install.
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
}

// hostNewVersionText is the notification for a release this host has not
// installed because its mode is notify.
func hostNewVersionText(tag, host string) string {
	return fmt.Sprintf("aiman %s available on %s (aiman host update)", tag, host)
}

// hostAppliedText reports one settled install. running is the version of the
// serve that settled it: equal to the target, serve took the update.
func hostAppliedText(a selfupdate.Attempt, running, host string) string {
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
	if a := v.Attempt; a != nil {
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

func formatHostUpdate(u server.HostUpdateResult) string {
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
