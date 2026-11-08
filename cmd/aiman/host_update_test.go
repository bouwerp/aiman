package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/selfupdate"
	"github.com/bouwerp/aiman/internal/server"
)

func TestNewHostUpdatesPaths(t *testing.T) {
	dir := t.TempDir()
	busy := func() int { return 0 }
	m := newHostUpdates(config.HostUpdateConfig{Mode: "bogus", CheckEvery: 10 * time.Second}, dir, "v0.34.0", hostUpdateDeps{Busy: []func() int{busy, busy}})
	if m.Version != "v0.34.0" || m.Policy.Mode != selfupdate.ModeNotify || m.Policy.CheckEvery != time.Minute {
		t.Fatalf("manager = %+v", m)
	}
	if m.StatePath != filepath.Join(dir, "host-update.json") || m.ResultPath != filepath.Join(dir, "host-update.result.json") ||
		m.LogPath != filepath.Join(dir, "logs", "host-update.log") {
		t.Fatalf("paths = %s %s %s", m.StatePath, m.ResultPath, m.LogPath)
	}
	if _, ok := m.Fetcher.(releaseFetcher); !ok || m.Runner == nil || len(m.Busy) != 2 {
		t.Fatalf("fetcher %T runner %v busy %d", m.Fetcher, m.Runner, len(m.Busy))
	}
	if off := newHostUpdates(config.HostUpdateConfig{Mode: "off"}, dir, "dev", hostUpdateDeps{}); off.Policy.Mode != selfupdate.ModeOff {
		t.Fatalf("off mode = %q", off.Policy.Mode)
	}
}

type emitted struct {
	session string
	kind    domain.NotifyKind
	text    string
}

type hostNotifier struct{ got []emitted }

func (f *hostNotifier) Emit(_ context.Context, sessionID, _ string, kind domain.NotifyKind, detail string) error {
	f.got = append(f.got, emitted{sessionID, kind, detail})
	return nil
}

func TestWireHostUpdateNotifyTexts(t *testing.T) {
	m := &selfupdate.Manager{Version: "v0.35.0"}
	n := &hostNotifier{}
	wireHostUpdateNotify(context.Background(), m, n, "remote1")
	m.OnNewVersion(selfupdate.Release{Tag: "v0.36.0"})
	m.OnApplied(selfupdate.Attempt{From: "v0.34.0", To: "v0.35.0", Status: selfupdate.AttemptOK})
	m.OnApplied(selfupdate.Attempt{From: "v0.34.0", To: "v0.35.0", Status: selfupdate.AttemptFailed, Exit: 2, Error: "gateway lockstep failed (exit 2)"})
	m.OnApplied(selfupdate.Attempt{From: "v0.35.0", To: "v0.36.0", Status: selfupdate.AttemptFailed, Error: "checksum mismatch"})
	want := []string{
		"aiman v0.36.0 available on remote1 (aiman host update)",
		"serve updated v0.34.0 → v0.35.0 on remote1",
		"serve update to v0.35.0 on remote1: gateway install failed",
		"serve update to v0.36.0 on remote1 failed: checksum mismatch",
	}
	if len(n.got) != len(want) {
		t.Fatalf("emitted %+v", n.got)
	}
	for i, e := range n.got {
		if e.session != "" || e.kind != domain.NotifyHostUpdate || e.text != want[i] {
			t.Errorf("emit %d = %+v, want %q", i, e, want[i])
		}
	}
}

func TestFormatHostVersion(t *testing.T) {
	got := formatHostVersion(server.HostVersionResult{
		Version: "v0.34.0", Latest: "v0.35.0", LatestReady: true, Mode: "auto", Status: "failed",
		CheckedAt: "2026-11-08T10:15:00Z",
		Attempt:   &server.HostUpdateTried{At: "2026-11-08T10:15:30Z", From: "v0.34.0", To: "v0.35.0", Status: "failed", Exit: 3, Error: "checksum"},
	})
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %q", got)
	}
	if !strings.HasPrefix(lines[0], "v0.34.0 · latest v0.35.0 (ready) · auto · failed · checked ") {
		t.Errorf("status line = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "attempt v0.34.0 → v0.35.0 failed at ") || !strings.HasSuffix(lines[1], "(exit 3): checksum") {
		t.Errorf("attempt line = %q", lines[1])
	}
	held := formatHostVersion(server.HostVersionResult{Version: "dev", Mode: "off", Status: "held"})
	if held != "dev · latest - · off · held · never checked\n" {
		t.Errorf("held = %q", held)
	}
}

func TestFormatHostUpdate(t *testing.T) {
	if got := formatHostUpdate(server.HostUpdateResult{Accepted: true, Target: "v0.35.0"}); got != "accepted: v0.35.0\n" {
		t.Errorf("accepted = %q", got)
	}
	if got := formatHostUpdate(server.HostUpdateResult{Reason: "busy"}); got != "refused: busy\n" {
		t.Errorf("refused = %q", got)
	}
}
