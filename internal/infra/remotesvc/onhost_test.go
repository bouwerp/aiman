package remotesvc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// runOnHost runs script under bash with HOME set to home and no user systemd.
func runOnHost(t *testing.T, home, script string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "XDG_RUNTIME_DIR=" + filepath.Join(home, "no-runtime")}
	out, err := cmd.Output()
	return string(out), err
}

func TestStateScriptNotInstalled(t *testing.T) {
	bashSyntaxOK(t, "state", StateScript(KindGateway))
	out, err := runOnHost(t, t.TempDir(), StateScript(KindTrigger))
	if err != nil || strings.TrimSpace(out) != "INSTALLED=0" {
		t.Fatalf("out %q err %v", out, err)
	}
}

func TestLogsScriptTailsTheNohupLog(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".aiman"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aiman", "gateway.log"), []byte("a\nb\nc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runOnHost(t, home, LogsScript(KindGateway, 2))
	if err != nil || out != LogSourceMarker+"file\nb\nc\n" {
		t.Fatalf("out %q err %v", out, err)
	}
}

func TestRestartScriptFailsWithNothingToRestart(t *testing.T) {
	if _, err := runOnHost(t, t.TempDir(), RestartScript(KindGateway)); err == nil {
		t.Fatal("restart with no unit and no supervisor succeeded")
	}
}

func TestRestartScriptKillsTheSupervisedChild(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".aiman"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A stand-in supervisor: its child is the "daemon"; when the child dies
	// it exits, which the test reads as the restart signal.
	sup := exec.Command("bash", "-c", "sleep 30 & wait $!; echo child-gone")
	var out strings.Builder
	sup.Stdout = &out
	if err := sup.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sup.Process.Kill() })
	pidFile := filepath.Join(home, ".aiman", "trigger.pid")
	if err := os.WriteFile(pidFile, []byte(itoa(sup.Process.Pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Wait for the child to exist.
	for range 50 {
		if o, _ := exec.Command("pgrep", "-P", itoa(sup.Process.Pid)).Output(); len(o) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got, err := runOnHost(t, home, RestartScript(KindTrigger)); err != nil {
		t.Fatalf("restart: %v %q", err, got)
	}
	if err := sup.Wait(); err != nil || !strings.Contains(out.String(), "child-gone") {
		t.Fatalf("supervisor: %v %q", err, out.String())
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
