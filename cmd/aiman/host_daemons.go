package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/infra/remotesvc"
	"github.com/bouwerp/aiman/internal/selfupdate"
	"github.com/bouwerp/aiman/internal/server"
)

const (
	// daemonReadTimeout bounds one daemon's state script; the script bounds
	// each command in it to 2s.
	daemonReadTimeout = 5 * time.Second
	// daemonCacheTTL is how long host.daemons answers from the last read.
	daemonCacheTTL = 5 * time.Second
	// daemonLogsTimeout bounds one log read.
	daemonLogsTimeout = 10 * time.Second
	// clkTck is USER_HZ, the unit of /proc/<pid>/stat start times; Linux
	// fixes it at 100 on every supported architecture.
	clkTck = 100
	// gatewayTokenFile is gateway.TokenFileName, named here so the aiman
	// binary does not link the gateway.
	gatewayTokenFile = "gateway-token"
)

// hostDaemons is serve's DaemonControl: it reads serve from its own process,
// the gateway and the trigger through remotesvc.StateScript, and restarts
// through the self-update manager's operations.
type hostDaemons struct {
	updates *selfupdate.Manager
	version string
	home    string
	// run runs a bash script and returns its stdout; nil means bash -c.
	run func(ctx context.Context, script string) (string, error)

	mu    sync.Mutex
	cache []server.DaemonState
	at    time.Time
}

var _ server.DaemonControl = (*hostDaemons)(nil)

func newHostDaemons(m *selfupdate.Manager, version, home string) *hostDaemons {
	return &hostDaemons{updates: m, version: version, home: home}
}

func (h *hostDaemons) bash(ctx context.Context, script string) (string, error) {
	if h.run != nil {
		return h.run(ctx, script)
	}
	out, err := exec.CommandContext(ctx, "bash", "-c", script).Output()
	return string(out), err
}

// Daemons returns serve, gateway and trigger, from a read at most
// daemonCacheTTL old unless refresh.
func (h *hostDaemons) Daemons(ctx context.Context, refresh bool) ([]server.DaemonState, error) {
	h.mu.Lock()
	if !refresh && h.cache != nil && time.Since(h.at) < daemonCacheTTL {
		rows := append([]server.DaemonState(nil), h.cache...)
		h.mu.Unlock()
		return h.withRestarts(rows), nil
	}
	h.mu.Unlock()

	rows := []server.DaemonState{h.serveRow()}
	var wg sync.WaitGroup
	others := []remotesvc.Kind{remotesvc.KindGateway, remotesvc.KindTrigger}
	read := make([]server.DaemonState, len(others))
	for i, k := range others {
		wg.Go(func() { read[i] = h.readDaemon(ctx, k) })
	}
	wg.Wait()
	rows = append(rows, read...)

	h.mu.Lock()
	h.cache, h.at = rows, time.Now()
	h.mu.Unlock()
	return h.withRestarts(append([]server.DaemonState(nil), rows...)), nil
}

// withRestarts adds the last restart operation to its daemon's row. It is
// read fresh, so polling after a restart sees it settle.
func (h *hostDaemons) withRestarts(rows []server.DaemonState) []server.DaemonState {
	if h.updates == nil {
		return rows
	}
	op := h.updates.Operation()
	if op == nil {
		return rows
	}
	k, ok := selfupdate.OperationKind(op.To)
	if !ok {
		return rows
	}
	for i := range rows {
		if rows[i].Kind == string(k) {
			rows[i].Restart = &server.DaemonRestart{Name: op.To, Status: string(op.Status), At: op.At, Exit: op.Exit}
		}
	}
	return rows
}

// serveRow is this process.
func (h *hostDaemons) serveRow() server.DaemonState {
	pid := os.Getpid()
	row := server.DaemonState{
		Kind: string(remotesvc.KindServe), Installed: true, Active: "active",
		Version: h.version, PID: pid, Driver: "none",
	}
	switch {
	case os.Getenv("INVOCATION_ID") != "":
		row.Driver = "systemd"
	case fileExists(filepath.Join(h.home, ".aiman", "serve.pid")):
		row.Driver = "nohup"
	}
	row.StartedAt, row.RSSBytes = procStats(pid)
	return row
}

// readDaemon runs StateScript for k and fills start time and RSS from /proc.
func (h *hostDaemons) readDaemon(ctx context.Context, k remotesvc.Kind) server.DaemonState {
	row := server.DaemonState{Kind: string(k), Driver: "none", Active: "unknown"}
	ctx, cancel := context.WithTimeout(ctx, daemonReadTimeout)
	defer cancel()
	out, err := h.bash(ctx, remotesvc.StateScript(k))
	if err != nil && out == "" {
		return row
	}
	kv := parseKV(out)
	if kv["INSTALLED"] != "1" {
		return server.DaemonState{Kind: string(k), Driver: "none", Active: "inactive"}
	}
	row.Installed = true
	row.Driver = orDefault(kv["DRIVER"], "none")
	row.Active = orDefault(kv["ACTIVE"], "inactive")
	row.Version = kv["VERSION"]
	if v, ok := remotesvc.ParseVersion(row.Version); ok {
		row.Version = v.String()
	}
	if pid, err := strconv.Atoi(kv["PID"]); err == nil && pid > 0 {
		row.PID = pid
		row.StartedAt, row.RSSBytes = procStats(pid)
	}
	return row
}

// Logs reads kind's journal or nohup log through remotesvc.LogsScript.
func (h *hostDaemons) Logs(ctx context.Context, kind string, lines int) (server.DaemonLogs, error) {
	ctx, cancel := context.WithTimeout(ctx, daemonLogsTimeout)
	defer cancel()
	out, err := h.bash(ctx, remotesvc.LogsScript(remotesvc.Kind(kind), lines))
	if err != nil && out == "" {
		return server.DaemonLogs{}, fmt.Errorf("read %s logs: %w", kind, err)
	}
	first, rest, _ := strings.Cut(out, "\n")
	src, ok := strings.CutPrefix(first, remotesvc.LogSourceMarker)
	if !ok {
		return server.DaemonLogs{}, fmt.Errorf("read %s logs: unexpected output", kind)
	}
	rest = strings.TrimRight(rest, "\n")
	logs := server.DaemonLogs{Source: src, Lines: []string{}}
	if rest != "" {
		logs.Lines = strings.Split(rest, "\n")
	}
	return logs, nil
}

// Restart starts restart-<kind> through the self-update manager.
func (h *hostDaemons) Restart(_ context.Context, kind string, force bool) (server.RestartOutcome, error) {
	if h.updates == nil {
		return server.RestartOutcome{}, errors.New("restart is not available on this host")
	}
	o := h.updates.StartOperation(selfupdate.RestartOperation(remotesvc.Kind(kind)), force)
	if o.Accepted {
		h.mu.Lock()
		h.cache = nil
		h.mu.Unlock()
	}
	return server.RestartOutcome{Accepted: o.Accepted, Reason: o.Reason}, nil
}

// RedactValues is the gateway token, read on each call and never logged.
func (h *hostDaemons) RedactValues() []string {
	b, err := os.ReadFile(filepath.Join(h.home, ".aiman", gatewayTokenFile)) //nolint:gosec // G304: fixed path under the home directory
	if err != nil {
		return nil
	}
	if tok := strings.TrimSpace(string(b)); tok != "" {
		return []string{tok}
	}
	return nil
}

func parseKV(out string) map[string]string {
	kv := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			kv[k] = strings.TrimSpace(v)
		}
	}
	return kv
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// procStats reads pid's start time and resident size from /proc. Either is
// zero when it cannot be read (another OS, or the process is gone).
func procStats(pid int) (time.Time, int64) {
	return procStart(pid), procRSS(pid)
}

func procStart(pid int) time.Time {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return time.Time{}
	}
	// The command name may hold spaces; fields start after its ')'.
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return time.Time{}
	}
	f := strings.Fields(s[i+1:])
	// starttime is field 22; f[0] is field 3.
	if len(f) < 20 {
		return time.Time{}
	}
	ticks, err := strconv.ParseInt(f[19], 10, 64)
	if err != nil {
		return time.Time{}
	}
	boot := bootTime()
	if boot.IsZero() {
		return time.Time{}
	}
	return boot.Add(time.Duration(ticks) * time.Second / clkTck).UTC()
}

func bootTime() time.Time {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "btime "); ok {
			if sec, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				return time.Unix(sec, 0)
			}
		}
	}
	return time.Time{}
}

func procRSS(pid int) int64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "VmRSS:"); ok {
			f := strings.Fields(v)
			if len(f) > 0 {
				if kb, err := strconv.ParseInt(f[0], 10, 64); err == nil {
					return kb << 10
				}
			}
		}
	}
	return 0
}
