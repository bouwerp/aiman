package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/remotesvc"
	"github.com/bouwerp/aiman/internal/server"
)

func hintModel(version string) *Model {
	m := eventModel()
	m.version = version
	m.daemons = map[string]domain.Daemon{}
	return m
}

func TestRefreshReleaseHintToastsOncePerTag(t *testing.T) {
	m := hintModel("v0.34.0")
	m.githubLatest = "v0.35.0"
	if cmd := m.refreshReleaseHint(); cmd == nil {
		t.Fatal("a new tag raised no toast")
	}
	if got, want := m.versionSlot(), "v0.34.0 · v0.35.0 available: aiman update"; got != want {
		t.Fatalf("versionSlot = %q, want %q", got, want)
	}
	if cmd := m.refreshReleaseHint(); cmd != nil {
		t.Fatal("the same tag toasted twice")
	}
	m.storeDaemon(domain.Daemon{RemoteHost: "regent0", Kind: string(remotesvc.KindServe), Version: "aiman v0.36.0"})
	if cmd := m.refreshReleaseHint(); cmd == nil {
		t.Fatal("a newer remote tag raised no toast")
	}
	if m.hint == nil || m.hint.Tag != "v0.36.0" || m.hint.Source != "remote" {
		t.Fatalf("hint = %+v, want v0.36.0 from remote", m.hint)
	}
}

func TestVersionSlotWithoutHint(t *testing.T) {
	m := hintModel("dev")
	m.githubLatest = "v0.35.0"
	if cmd := m.refreshReleaseHint(); cmd != nil {
		t.Fatal("a dev TUI got a hint toast")
	}
	if got := m.versionSlot(); got != "dev" {
		t.Fatalf("versionSlot = %q, want dev", got)
	}
}

func TestApplyGitHubReleaseKeepsLastTagOnError(t *testing.T) {
	m := hintModel("v0.34.0")
	m.githubLatest = "v0.35.0"
	if cmd := m.applyGitHubRelease(githubReleaseMsg{}); cmd == nil {
		t.Fatal("no next check scheduled")
	}
	if m.githubLatest != "v0.35.0" {
		t.Fatalf("githubLatest = %q after a failed check", m.githubLatest)
	}
	m.applyGitHubRelease(githubReleaseMsg{tag: "v0.36.0"})
	if m.hint == nil || m.hint.Tag != "v0.36.0" || m.hint.Source != "github" {
		t.Fatalf("hint = %+v, want v0.36.0 from github", m.hint)
	}
}

func TestHintLineShowsReleaseHint(t *testing.T) {
	m := hintModel("v0.34.0")
	m.githubLatest = "v0.35.0"
	m.refreshReleaseHint()
	if got := m.hintLine(120); !strings.Contains(got, "v0.35.0 available: aiman update") {
		t.Fatalf("hint line %q lacks the release hint", got)
	}
}

func TestNotifyToastHostUpdateWithoutTextStaysQuiet(t *testing.T) {
	m := eventModel()
	if cmd := m.notifyToast(server.SessionEvent{Type: "notify", Kind: "host_update"}); cmd != nil {
		t.Fatal("host_update with no text raised a toast")
	}
}

// The gateway's probe carries no attempt, but the host's install updates it too.
func TestAutoUpdateGatewayWaitsForHostSelfUpdate(t *testing.T) {
	m := autoUpdateModel("v0.35.0")
	serve := runningServe("aiman v0.35.0")
	serve.SelfUpdate = &domain.SelfUpdateAttempt{Status: "started", To: "v0.35.0", At: time.Now()}
	m.daemons = map[string]domain.Daemon{domain.DaemonKey("regent0", string(remotesvc.KindServe)): serve}
	gw := runningServe("aiman v0.34.0")
	gw.Kind = string(remotesvc.KindGateway)
	if cmd := m.maybeAutoUpdateServe(gw); cmd != nil {
		t.Fatal("the laptop updated a gateway during the host's own install")
	}
}

func TestHostSelfUpdatingIgnoresSettledAttempts(t *testing.T) {
	for _, status := range []string{"ok", "failed"} {
		if hostSelfUpdating(&domain.SelfUpdateAttempt{Status: status, At: time.Now()}) {
			t.Fatalf("a %s attempt counted as in flight", status)
		}
	}
}
