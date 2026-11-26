//go:build feedaccept

package server

import (
	"context"
	"strings"
	"testing"
)

// Acceptance tests for the feed collector under daemon control (event-feed
// P4): host.daemons carries its row, host.logs and host.restart accept it.

// feedDaemons is fakeDaemons with an installed, running collector row.
type feedDaemons struct{ fakeDaemons }

func (f *feedDaemons) Daemons(ctx context.Context, refresh bool) ([]DaemonState, error) {
	rows, err := f.fakeDaemons.Daemons(ctx, refresh)
	return append(rows, DaemonState{Kind: "feed", Installed: true, Driver: "systemd", Active: "active", Version: "v0.48.0", PID: 300}), err
}

func TestHostDaemonsListsFeed(t *testing.T) {
	srv := hostUpdateServer(t)
	srv.SetDaemonControl(&feedDaemons{})
	var res hostDaemonsResult
	rpcOK(t, srv, "", "host.daemons", map[string]any{}, &res)
	var found bool
	for _, d := range res.Daemons {
		if d.Kind == "feed" {
			found = d.Installed && d.Active == "active" && d.PID == 300
		}
	}
	if !found {
		t.Fatalf("host.daemons has no running feed row: %+v", res.Daemons)
	}
}

func TestHostLogsAndRestartAcceptFeed(t *testing.T) {
	f := &fakeDaemons{lines: []string{"feed: slack polled 12 conversations"}, outcome: RestartOutcome{Accepted: true}}
	srv := daemonServer(t, f)

	var logs hostLogsResult
	rpcOK(t, srv, "", "host.logs", map[string]any{"daemon": "feed", "lines": 50}, &logs)
	if logs.Daemon != "feed" || len(logs.Lines) != 1 || len(f.logKinds) != 1 || f.logKinds[0] != "feed" || f.logLines[0] != 50 {
		t.Fatalf("logs = %+v, control saw %v %v", logs, f.logKinds, f.logLines)
	}

	var res hostRestartResult
	rpcOK(t, srv, "", "host.restart", map[string]any{"daemon": "feed"}, &res)
	if !res.Accepted || res.Daemon != "feed" || len(f.restarts) != 1 || f.restarts[0] != "feed" {
		t.Fatalf("restart = %+v, control saw %v", res, f.restarts)
	}

	// Sessions are still refused, and the refusal for a bad daemon names feed.
	for _, m := range []string{"host.logs", "host.restart"} {
		wantCode(t, rpc(t, srv, hostUpdateCaller, m, map[string]any{"daemon": "feed"}), CodeNeedsOperator)
		resp := rpc(t, srv, "", m, map[string]any{"daemon": "sshd"})
		wantCode(t, resp, CodeInvalidParams)
		if !strings.Contains(resp.Error.Message, "feed") {
			t.Errorf("%s refusal %q does not list feed", m, resp.Error.Message)
		}
	}
	if len(f.restarts) != 1 {
		t.Fatalf("refused calls reached the control: %v", f.restarts)
	}
}
