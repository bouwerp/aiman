//go:build !windows

package limits

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// readSixKeys consumes the /usage text with key, which works for any byte but CR
// whichever way the fake reads the terminal.
const readSixKeys = `for i in 1 2 3 4 5 6; do key; done`

func TestProbeSendsNoEnterWhenADialogAppearsWhileTyping(t *testing.T) {
	body := `draw "$COMPOSER"; ` + readSixKeys + `; printf '\033[2J\033[HUpdate available\r\n  Update now   enter\r\n'; while key; do :; done`
	cfg, dir := probeConfig(t, fakeGrok(t, t.TempDir(), body))
	cfg.Timeout = 2 * time.Second
	if r := RunGrokProbe(context.Background(), cfg); r.State != StateUnknown {
		t.Fatalf("reading %+v", r)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "input.log"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != grokUsageCommand {
		t.Fatalf("keys sent = %q, want only the command with no Enter", raw)
	}
}

func TestProbeKillsTheTreeOnCancel(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	cfg, _ := probeConfig(t, fakeGrok(t, dir, fmt.Sprintf(`sleep 300 & echo $! > %q; while :; do sleep 1; done`, pidFile)))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	if r := RunGrokProbe(ctx, cfg); r.State != StateUnknown {
		t.Fatalf("reading %+v", r)
	}
	if took := time.Since(start); took > cfg.Timeout/2 {
		t.Fatalf("took %v after the context ended", took)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	for range 50 {
		if !pidAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatal("grandchild survived a cancelled probe")
}

func TestProbeNudgesTheTerminalSize(t *testing.T) {
	dir := t.TempDir()
	body := fmt.Sprintf(`trap 'stty size >> %q/winch.log' WINCH; draw "$COMPOSER"; while :; do sleep 0.05; done`, dir)
	cfg, _ := probeConfig(t, fakeGrok(t, dir, body))
	cfg.Timeout = 2 * time.Second
	RunGrokProbe(context.Background(), cfg)
	raw, err := os.ReadFile(filepath.Join(dir, "winch.log"))
	if err != nil {
		t.Fatalf("the CLI saw no size change: %v", err)
	}
	if got := string(raw); !strings.Contains(got, "40 120") {
		t.Fatalf("size after the nudge = %q, want it back at 40 120", got)
	}
}
