package remotesvc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// GatewayRepairScript installs the gateway at serve's tag and never touches
// serve; a failed install stops it before any restart.

func TestGatewayRepairScriptIsGatewayOnly(t *testing.T) {
	s := GatewayRepairScript("v0.42.0")
	if !strings.HasPrefix(s, "set -e\n") {
		t.Fatalf("script must start with set -e:\n%s", s)
	}
	if !strings.Contains(s, KindGateway.InstallPipeAt("v0.42.0")) {
		t.Fatalf("script lacks the gateway install at v0.42.0:\n%s", s)
	}
	if strings.Contains(s, KindServe.InstallPipeAt("v0.42.0")) || strings.Contains(s, "aiman-serve") {
		t.Fatalf("script touches serve:\n%s", s)
	}
}

func TestGatewayRepairScriptStopsOnAFailedInstall(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	stubs := filepath.Join(home, "stubs")
	for _, d := range []string{bin, stubs} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(home, "touched")
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil { //nolint:gosec // test stub must be executable
			t.Fatal(err)
		}
	}
	write(filepath.Join(bin, "aiman-gateway"), "exit 0")
	// The download fails; nothing after it may run.
	write(filepath.Join(stubs, "curl"), "exit 22")
	for _, name := range []string{"pgrep", "pkill", "systemctl", "nohup", "kill"} {
		write(filepath.Join(stubs, name), "echo "+name+" >> "+marker+"; exit 0")
	}
	// pgrep says the gateway runs, so a script that ignored the failure would restart it.
	write(filepath.Join(stubs, "pgrep"), "exit 0")

	cmd := exec.Command("bash", "-c", GatewayRepairScript("v0.42.0"))
	cmd.Env = []string{"HOME=" + home, "PATH=" + stubs + ":/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("script exited 0 after a failed install:\n%s", out)
	}
	if b, _ := os.ReadFile(marker); len(b) > 0 {
		t.Fatalf("script went on to stop or start the gateway after a failed install: %s\n%s", b, out)
	}
}
