package remotesvc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Gateway hardening: a crashed gateway comes back on its own, starting it
// always prefers a systemd user unit, and its log does not grow without bound.

// sandbox is a HOME and a PATH holding every tool in /usr/bin and /bin except
// the ones a test fakes or hides, so a script run here cannot touch real
// services or processes.
type sandbox struct {
	home, bin string
}

var hiddenTools = map[string]bool{"systemctl": true, "loginctl": true, "journalctl": true, "pkill": true, "pgrep": true, "curl": true}

func newSandbox(t *testing.T) sandbox {
	t.Helper()
	dir := t.TempDir()
	s := sandbox{home: filepath.Join(dir, "home"), bin: filepath.Join(dir, "bin")}
	for _, d := range []string{s.home, s.bin, filepath.Join(s.home, ".local", "bin")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, src := range []string{"/usr/bin", "/bin"} {
		entries, _ := os.ReadDir(src)
		for _, e := range entries {
			if hiddenTools[e.Name()] {
				continue
			}
			_ = os.Symlink(filepath.Join(src, e.Name()), filepath.Join(s.bin, e.Name()))
		}
	}
	// pkill and pgrep only log: a real pkill -f could hit processes outside the sandbox.
	s.fake(t, "pkill", `echo "pkill $*" >> "$HOME/calls"; exit 1`)
	s.fake(t, "pgrep", `echo "pgrep $*" >> "$HOME/calls"; exit 1`)
	return s
}

func (s sandbox) fake(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.bin, name), []byte("#!/bin/bash\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (s sandbox) gatewayBinary(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(s.home, ".local", "bin", "aiman-gateway")
	if err := os.WriteFile(p, []byte("#!/bin/bash\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (s sandbox) run(t *testing.T, script string) string {
	t.Helper()
	cmd := exec.Command(filepath.Join(s.bin, "bash"), "-c", script)
	cmd.Env = []string{"HOME=" + s.home, "PATH=" + s.bin, "XDG_RUNTIME_DIR=" + filepath.Join(s.home, "run")}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	return string(out)
}

func (s sandbox) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(s.home, name))
	return string(b)
}

func lineCount(s string) int { return strings.Count(s, "\n") }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// A host with user systemd but no unit file yet (the gateway was first started
// with nohup) gets the unit written and started, never a nohup process.
func TestGatewayStartWritesUnitWhenMissing(t *testing.T) {
	s := newSandbox(t)
	s.fake(t, "systemctl", `echo "systemctl $*" >> "$HOME/calls"; exit 0`)
	s.fake(t, "loginctl", `echo "loginctl $*" >> "$HOME/calls"; exit 0`)
	s.gatewayBinary(t, `echo started >> "$HOME/runs"; sleep 30`)

	s.run(t, StartScript(KindGateway))

	unit := s.read(".config/systemd/user/aiman-gateway.service")
	if !strings.Contains(unit, "Restart=") {
		t.Fatalf("unit not written or has no Restart=:\n%s", unit)
	}
	calls := s.read("calls")
	if !strings.Contains(calls, "systemctl --user start aiman-gateway.service") && !strings.Contains(calls, "systemctl --user enable --now aiman-gateway.service") {
		t.Fatalf("unit was not started; calls:\n%s", calls)
	}
	time.Sleep(300 * time.Millisecond)
	if runs := s.read("runs"); runs != "" {
		t.Fatalf("gateway also started outside systemd: %q", runs)
	}
}

// Under systemd a panicking gateway (exit 2) must be restarted.
func TestGatewayUnitRestartsAfterPanic(t *testing.T) {
	unit := UnitFile(KindGateway)
	if !strings.Contains(unit, "Restart=always") && !strings.Contains(unit, "Restart=on-failure") {
		t.Fatalf("gateway unit does not restart on failure:\n%s", unit)
	}
}

// Without user systemd, the nohup fallback restarts a gateway that exits.
func TestGatewayNohupFallbackRestartsCrashes(t *testing.T) {
	s := newSandbox(t)
	s.gatewayBinary(t, `echo run >> "$HOME/runs"; exit 2`)
	t.Cleanup(func() {
		cmd := exec.Command(filepath.Join(s.bin, "bash"), "-c", StopScript(KindGateway))
		cmd.Env = []string{"HOME=" + s.home, "PATH=" + s.bin}
		_ = cmd.Run()
	})

	s.run(t, StartScript(KindGateway))
	waitFor(t, "a second gateway run after a crash", func() bool { return lineCount(s.read("runs")) >= 2 })

	// Stop ends the supervision: no run starts after it.
	s.run(t, StopScript(KindGateway))
	time.Sleep(500 * time.Millisecond)
	before := lineCount(s.read("runs"))
	time.Sleep(5 * time.Second)
	if after := lineCount(s.read("runs")); after != before {
		t.Fatalf("gateway kept restarting after stop: %d runs, then %d", before, after)
	}
}

// Starting rotates a gateway log over 10 MiB, keeping one older copy.
func TestGatewayStartRotatesLargeLog(t *testing.T) {
	s := newSandbox(t)
	s.gatewayBinary(t, `sleep 30`)
	t.Cleanup(func() {
		cmd := exec.Command(filepath.Join(s.bin, "bash"), "-c", StopScript(KindGateway))
		cmd.Env = []string{"HOME=" + s.home, "PATH=" + s.bin}
		_ = cmd.Run()
	})
	logPath := filepath.Join(s.home, ".aiman", "gateway.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, make([]byte, 11<<20), 0o644); err != nil {
		t.Fatal(err)
	}

	s.run(t, StartScript(KindGateway))

	fi, err := os.Stat(logPath)
	if err == nil && fi.Size() >= 10<<20 {
		t.Fatalf("gateway.log still %d bytes after start", fi.Size())
	}
	matches, _ := filepath.Glob(logPath + ".*")
	if len(matches) == 0 {
		t.Fatal("no rotated copy of gateway.log kept")
	}
}
