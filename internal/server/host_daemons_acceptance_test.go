package server

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// Acceptance tests for daemon-control piece A (host.daemons, host.logs,
// host.restart). Do not edit.

// fakeDaemons is a DaemonControl that records its calls.
type fakeDaemons struct {
	mu       sync.Mutex
	refresh  []bool
	logKinds []string
	logLines []int
	restarts []string
	forces   []bool
	lines    []string
	outcome  RestartOutcome
}

func (f *fakeDaemons) Daemons(_ context.Context, refresh bool) ([]DaemonState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refresh = append(f.refresh, refresh)
	at := time.Date(2026, 11, 13, 7, 0, 0, 0, time.UTC)
	return []DaemonState{
		{Kind: "serve", Installed: true, Driver: "systemd", Active: "active", Version: "v0.47.0", PID: 100, StartedAt: at, RSSBytes: 2 << 30},
		{Kind: "gateway", Installed: true, Driver: "systemd", Active: "active", Version: "v0.47.0", PID: 200, StartedAt: at, RSSBytes: 64 << 20,
			Restart: &DaemonRestart{Name: "restart-gateway", Status: "ok", At: at}},
		{Kind: "trigger", Installed: false},
	}, nil
}

func (f *fakeDaemons) Logs(_ context.Context, kind string, lines int) (DaemonLogs, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logKinds = append(f.logKinds, kind)
	f.logLines = append(f.logLines, lines)
	return DaemonLogs{Source: "journal", Lines: append([]string(nil), f.lines...)}, nil
}

func (f *fakeDaemons) Restart(_ context.Context, kind string, force bool) (RestartOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarts = append(f.restarts, kind)
	f.forces = append(f.forces, force)
	return f.outcome, nil
}

func daemonServer(t *testing.T, f *fakeDaemons) *Server {
	t.Helper()
	srv := hostUpdateServer(t)
	if f != nil {
		srv.SetDaemonControl(f)
	}
	return srv
}

type hostDaemonsResult struct {
	Type    string `json:"type"`
	Host    string `json:"host"`
	Daemons []struct {
		Kind      string `json:"kind"`
		Installed bool   `json:"installed"`
		Driver    string `json:"driver"`
		Active    string `json:"active"`
		Version   string `json:"version"`
		PID       int    `json:"pid"`
		StartedAt string `json:"started_at"`
		RSSBytes  int64  `json:"rss_bytes"`
		Restart   *struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			At     string `json:"at"`
		} `json:"restart"`
	} `json:"daemons"`
}

func TestHostDaemonsReportsEachDaemon(t *testing.T) {
	f := &fakeDaemons{}
	srv := daemonServer(t, f)
	var res hostDaemonsResult
	rpcOK(t, srv, "", "host.daemons", map[string]any{}, &res)
	if res.Type != "host_daemons" || len(res.Daemons) != 3 {
		t.Fatalf("result = %+v", res)
	}
	gw := res.Daemons[1]
	if gw.Kind != "gateway" || gw.PID != 200 || gw.RSSBytes != 64<<20 || gw.StartedAt == "" || gw.Restart == nil || gw.Restart.Status != "ok" {
		t.Fatalf("gateway row = %+v", gw)
	}
	if res.Daemons[2].Installed {
		t.Fatalf("trigger row = %+v", res.Daemons[2])
	}
	rpcOK(t, srv, "", "host.daemons", map[string]any{"refresh": true}, &res)
	if len(f.refresh) != 2 || f.refresh[0] || !f.refresh[1] {
		t.Fatalf("refresh flags = %v", f.refresh)
	}
	// A session may read state: it is a read method.
	rpcOK(t, srv, hostUpdateCaller, "host.daemons", map[string]any{}, &res)
}

func TestHostDaemonsUnavailableWithoutControl(t *testing.T) {
	srv := daemonServer(t, nil)
	for _, m := range []string{"host.daemons", "host.logs", "host.restart"} {
		wantCode(t, rpc(t, srv, "", m, map[string]any{"daemon": "serve"}), CodeUnavailable)
	}
}

type hostLogsResult struct {
	Type     string   `json:"type"`
	Daemon   string   `json:"daemon"`
	Source   string   `json:"source"`
	Lines    []string `json:"lines"`
	Redacted int      `json:"redacted"`
}

func TestHostLogsDefaultsCapsAndRedacts(t *testing.T) {
	f := &fakeDaemons{lines: []string{
		"serve: started",
		"slack sink: posting with xoxb-1234567890-abcdefghijkl",
		"notify: webhook https://hooks.slack.com/services/T000/B000/XXXXXXXXXXXXXXXX failed",
		strings.Repeat("x", 5000),
	}}
	srv := daemonServer(t, f)
	var res hostLogsResult
	rpcOK(t, srv, "", "host.logs", map[string]any{"daemon": "serve"}, &res)
	if res.Type != "host_logs" || res.Daemon != "serve" || res.Source != "journal" || len(res.Lines) != 4 {
		t.Fatalf("result = %+v", res)
	}
	if f.logLines[0] != 200 {
		t.Fatalf("default lines = %d, want 200", f.logLines[0])
	}
	joined := strings.Join(res.Lines, "\n")
	if strings.Contains(joined, "xoxb-1234567890") || strings.Contains(joined, "XXXXXXXXXXXXXXXX") || res.Redacted != 2 {
		t.Fatalf("not redacted: %q (redacted %d)", joined, res.Redacted)
	}
	if len(res.Lines[3]) > 2048 {
		t.Fatalf("long line not capped: %d bytes", len(res.Lines[3]))
	}
	rpcOK(t, srv, "", "host.logs", map[string]any{"daemon": "gateway", "lines": 5000}, &res)
	if f.logKinds[1] != "gateway" || f.logLines[1] != 1000 {
		t.Fatalf("calls = %v %v, want gateway capped at 1000", f.logKinds, f.logLines)
	}
}

func TestHostLogsAndRestartRefuseSessionsAndBadDaemons(t *testing.T) {
	f := &fakeDaemons{outcome: RestartOutcome{Accepted: true}}
	srv := daemonServer(t, f)
	for _, m := range []string{"host.logs", "host.restart"} {
		wantCode(t, rpc(t, srv, hostUpdateCaller, m, map[string]any{"daemon": "serve"}), CodeNeedsOperator)
		wantCode(t, sessionPeerCall(t, srv, m), CodeNeedsOperator)
		wantCode(t, rpc(t, srv, "", m, map[string]any{"daemon": "sshd"}), CodeInvalidParams)
		wantCode(t, rpc(t, srv, "", m, map[string]any{}), CodeInvalidParams)
	}
	if len(f.restarts) != 0 || len(f.logKinds) != 0 {
		t.Fatalf("refused calls reached the control: %v %v", f.restarts, f.logKinds)
	}
}

type hostRestartResult struct {
	Type     string `json:"type"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason"`
	Daemon   string `json:"daemon"`
}

func TestHostRestartPassesForceAndAnswers(t *testing.T) {
	f := &fakeDaemons{outcome: RestartOutcome{Accepted: false, Reason: "busy"}}
	srv := daemonServer(t, f)
	var res hostRestartResult
	rpcOK(t, srv, "", "host.restart", map[string]any{"daemon": "serve"}, &res)
	if res.Type != "host_restart" || res.Accepted || res.Reason != "busy" || res.Daemon != "serve" {
		t.Fatalf("result = %+v", res)
	}
	f.outcome = RestartOutcome{Accepted: true}
	rpcOK(t, srv, "", "host.restart", map[string]any{"daemon": "gateway", "force": true}, &res)
	if !res.Accepted || res.Daemon != "gateway" {
		t.Fatalf("result = %+v", res)
	}
	if want := []string{"serve", "gateway"}; strings.Join(f.restarts, ",") != strings.Join(want, ",") || f.forces[0] || !f.forces[1] {
		t.Fatalf("restarts = %v forces = %v", f.restarts, f.forces)
	}
}
