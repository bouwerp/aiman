package ui

import (
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/remotesvc"
)

func autoUpdateModel(localVersion string) *Model {
	return &Model{
		cfg:           &config.Config{Remotes: []config.Remote{{Host: "regent0", User: "code"}}},
		version:       localVersion,
		serveUpdateAt: map[string]time.Time{},
	}
}

func runningServe(version string) domain.Daemon {
	return domain.Daemon{
		RemoteHost: "regent0",
		Kind:       string(remotesvc.KindServe),
		Status:     domain.DaemonStatusRunning,
		Version:    version,
	}
}

// Nearly every runtime fix lives in serve rather than the TUI, so a remote left
// behind loses them while still looking healthy.
func TestAutoUpdateRunsForAnOlderGateway(t *testing.T) {
	m := autoUpdateModel("v0.19.10")
	d := domain.Daemon{
		RemoteHost: "regent0",
		Kind:       string(remotesvc.KindGateway),
		Status:     domain.DaemonStatusRunning,
		Version:    "aiman-gateway v0.19.4",
	}
	if cmd := m.maybeAutoUpdateServe(d); cmd == nil {
		t.Fatal("a gateway behind the client should be updated")
	}
}

func TestAutoUpdateRunsForAnOlderServe(t *testing.T) {
	m := autoUpdateModel("v0.19.10")
	if cmd := m.maybeAutoUpdateServe(runningServe("aiman v0.19.4 (built x)")); cmd == nil {
		t.Fatal("a serve behind the client should be updated")
	}
	if _, recorded := m.serveUpdateAt["regent0\x00"+string(remotesvc.KindServe)]; !recorded {
		t.Error("the attempt should be recorded so it is not retried on every probe")
	}
}

func TestAutoUpdateLeavesTheseAlone(t *testing.T) {
	cases := []struct {
		name   string
		local  string
		daemon domain.Daemon
	}{
		{"already current", "v0.19.10", runningServe("aiman v0.19.10")},
		// Never downgrade someone's host to match an older laptop.
		{"remote is ahead", "v0.19.10", runningServe("aiman v0.20.0")},
		// A locally-built client has no release to offer.
		{"client is a dev build", "dev", runningServe("aiman v0.19.4")},
		// No aiman on the remote is an install decision, not an update.
		{"remote has none", "v0.19.10", runningServe("missing")},
		{"probe gave no version", "v0.19.10", runningServe("")},
		// The trigger daemon runs autonomous work on a schedule; restarting it
		// unasked could interrupt a run.
		{"trigger daemon", "v0.19.10", domain.Daemon{
			RemoteHost: "regent0", Kind: string(remotesvc.KindTrigger),
			Status: domain.DaemonStatusRunning, Version: "aiman-trigger v0.19.4",
		}},
		// A stopped serve is an install/start decision.
		{"serve stopped", "v0.19.10", domain.Daemon{
			RemoteHost: "regent0", Kind: string(remotesvc.KindServe),
			Status: domain.DaemonStatusStopped, Version: "aiman v0.19.4",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := autoUpdateModel(tc.local)
			if cmd := m.maybeAutoUpdateServe(tc.daemon); cmd != nil {
				t.Errorf("%s should not trigger an update", tc.name)
			}
			if len(m.serveUpdateAt) != 0 {
				t.Errorf("%s should not record an attempt", tc.name)
			}
		})
	}
}

// A probe arrives every few seconds; a failing update must not be retried on
// each one.
func TestAutoUpdateBacksOffBetweenAttempts(t *testing.T) {
	m := autoUpdateModel("v0.19.10")
	d := runningServe("aiman v0.19.4")
	if cmd := m.maybeAutoUpdateServe(d); cmd == nil {
		t.Fatal("first attempt should run")
	}
	if cmd := m.maybeAutoUpdateServe(d); cmd != nil {
		t.Error("a second probe moments later must not retry")
	}
	// Once the backoff expires it tries again — the remote may have been fixed.
	m.serveUpdateAt["regent0\x00"+string(remotesvc.KindServe)] = time.Now().Add(-autoUpdateRetryAfter - time.Minute)
	if cmd := m.maybeAutoUpdateServe(d); cmd == nil {
		t.Error("after the backoff the update should be retried")
	}
}

// Opt-out has to be possible, and absent must mean enabled.
func TestAutoUpdateRespectsTheConfigFlag(t *testing.T) {
	m := autoUpdateModel("v0.19.10")
	if !m.cfg.AutoUpdateRemotes() {
		t.Fatal("an absent flag should mean enabled")
	}
	off := false
	m.cfg.Features.AutoUpdateRemotes = &off
	if cmd := m.maybeAutoUpdateServe(runningServe("aiman v0.19.4")); cmd != nil {
		t.Error("the flag should switch auto-updating off")
	}
	on := true
	m.cfg.Features.AutoUpdateRemotes = &on
	if cmd := m.maybeAutoUpdateServe(runningServe("aiman v0.19.4")); cmd == nil {
		t.Error("explicitly on should update")
	}
}
