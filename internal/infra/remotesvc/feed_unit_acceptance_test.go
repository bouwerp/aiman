//go:build feedaccept || feedaccept_p4

package remotesvc

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Acceptance tests for the feed collector daemon (event-feed P4, design S15).

func TestFeedKindIdentity(t *testing.T) {
	k := KindFeed
	if k != "feed" {
		t.Fatalf("kind %q", k)
	}
	for what, c := range map[string][2]string{
		"unit":   {k.Unit(), "aiman-feed"},
		"binary": {k.Binary(), "aiman"},
		"exec":   {k.ExecLine(), "%h/.local/bin/aiman feed collect"},
		"log":    {k.LogFile(), "$HOME/.aiman/feed.log"},
		"pid":    {k.PidFile(), "$HOME/.aiman/feed.pid"},
	} {
		if c[0] != c[1] {
			t.Errorf("%s = %q, want %q", what, c[0], c[1])
		}
	}
}

func TestFeedUnitFile(t *testing.T) {
	u := UnitFile(KindFeed)
	for _, want := range []string{"Description=Aiman feed", "ExecStart=%h/.local/bin/aiman feed collect", "Restart=on-failure", "WantedBy=default.target"} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q:\n%s", want, u)
		}
	}
	if strings.Contains(u, "KillMode") {
		t.Errorf("feed unit must keep default cgroup cleanup:\n%s", u)
	}
	if strings.Contains(u, "aiman serve") {
		t.Errorf("feed unit runs serve:\n%s", u)
	}
}

func TestFeedProcPattern(t *testing.T) {
	re := regexp.MustCompile(KindFeed.procPattern())
	for _, cmd := range []string{
		"/home/u/.local/bin/aiman feed collect",
		"aiman feed collect",
	} {
		if !re.MatchString(cmd) {
			t.Errorf("feed pattern misses %q", cmd)
		}
	}
	for _, cmd := range []string{
		"/home/u/.local/bin/aiman serve",
		"/home/u/.local/bin/aiman pty hold --root /home/u/.aiman --id abc123",
		"/home/u/.local/bin/aiman feed status",
		"tail -f /home/u/.aiman/feed.log",
		"/home/u/.local/bin/aiman-gateway",
		"/home/u/.local/bin/aiman-trigger",
	} {
		if re.MatchString(cmd) {
			t.Errorf("feed pattern matches %q", cmd)
		}
	}
	// No other daemon's pattern matches the collector, so stopping one
	// never stops it.
	for _, k := range []Kind{KindServe, KindGateway, KindTrigger} {
		if regexp.MustCompile(k.procPattern()).MatchString("/home/u/.local/bin/aiman feed collect") {
			t.Errorf("%s pattern matches the feed collector", k)
		}
	}
}

func TestFeedScriptsTouchOnlyTheCollector(t *testing.T) {
	start := nohupStart(KindFeed)
	if !strings.Contains(start, "$HOME/.local/bin/aiman feed collect") || strings.Contains(start, "aiman serve") {
		t.Fatalf("nohup start:\n%s", start)
	}
	if !strings.Contains(start, "$HOME/.aiman/feed.log") || !strings.Contains(start, "$HOME/.aiman/feed.pid") {
		t.Fatalf("nohup start does not use the feed log and pid:\n%s", start)
	}
	stop := StopScript(KindFeed)
	if strings.Contains(stop, KindServe.procPattern()) || !strings.Contains(stop, KindFeed.procPattern()) {
		t.Fatalf("feed stop:\n%s", stop)
	}
	if probe := ProbeScript(KindFeed); strings.Contains(probe, "aiman.sock") {
		t.Fatalf("feed probe must not claim serve's socket:\n%s", probe)
	}
	for name, s := range map[string]string{
		"restart": RestartScript(KindFeed),
		"logs":    LogsScript(KindFeed, 50),
		"state":   StateScript(KindFeed),
	} {
		if !strings.Contains(s, "aiman-feed") {
			t.Errorf("%s script does not name aiman-feed:\n%s", name, s)
		}
	}
}

// Without user systemd the nohup supervisor runs the collector as
// `aiman feed collect`.
func TestFeedNohupFallbackRunsCollector(t *testing.T) {
	s := newSandbox(t)
	bin := filepath.Join(s.home, ".local", "bin", "aiman")
	if err := os.WriteFile(bin, []byte("#!/bin/bash\necho \"$*\" >> \"$HOME/runs\"; sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd := exec.Command(filepath.Join(s.bin, "bash"), "-c", StopScript(KindFeed))
		cmd.Env = []string{"HOME=" + s.home, "PATH=" + s.bin}
		_ = cmd.Run()
	})
	s.run(t, StartScript(KindFeed))
	waitFor(t, "the collector to start", func() bool { return strings.Contains(s.read("runs"), "feed collect") })
}

// A serve update restarts the collector after serve, only when it was
// running: it is the same binary, so the new one must be picked up.
func TestServeUpdateRestartsRunningCollector(t *testing.T) {
	up := UpdateScript(KindServe, testTag)
	check := "pgrep -f '" + KindFeed.procPattern() + "'"
	i := strings.Index(up, check)
	if i < 0 {
		t.Fatalf("serve update does not check for a running collector:\n%s", up)
	}
	serveStart := strings.Index(up, "systemctl --user start aiman-serve.service")
	feedStart := strings.LastIndex(up, "aiman-feed.service")
	if serveStart < 0 || feedStart < serveStart || i < serveStart {
		t.Fatalf("collector restart is not after serve's start (serve %d, check %d, feed %d):\n%s", serveStart, i, feedStart, up)
	}
	for _, k := range []Kind{KindGateway, KindTrigger} {
		if strings.Contains(UpdateScript(k, testTag), "aiman-feed") {
			t.Errorf("%s update touches the collector", k)
		}
	}
}
