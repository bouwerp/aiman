package limits

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestGrokClassifiesRealScreens(t *testing.T) {
	cases := map[string]GrokScreen{"grok-trust.txt": GrokTrust, "grok-composer.txt": GrokComposer, "grok-usage.txt": GrokUsage}
	for name, want := range cases {
		if got := ClassifyGrokScreen(fixture(t, name)); got != want {
			t.Errorf("%s = %d, want %d", name, got, want)
		}
	}
}

func TestGrokClassifiesAnythingElseAsOther(t *testing.T) {
	composer := fixture(t, "grok-composer.txt")
	cases := map[string]string{
		"typed text in the composer": strings.Replace(composer, "❯", "❯ hello there", 1),
		"update dialog":              "Update available\n  Update now   enter\n  Skip         esc\n",
		"dialog over a composer":     "Update available\n  Update now   enter\n  Skip         esc\n" + composer,
		"empty":                      "",
	}
	for name, screen := range cases {
		if got := ClassifyGrokScreen(screen); got != GrokOther {
			t.Errorf("%s = %d, want GrokOther", name, got)
		}
	}
}

func TestGrokParsesTheColouredPanel(t *testing.T) {
	r, ok := ParseGrokUsage(fixture(t, "grok-usage.txt"))
	if !ok || r.Plan != "SuperGrok Plus" || len(r.Windows) != 1 {
		t.Fatalf("reading %+v ok=%v", r, ok)
	}
	w := r.Windows[0]
	if w.Label != "week" || w.UsedPercent != 19 || w.ResetsText != "November 11, 13:44" || !w.ResetsAt.IsZero() {
		t.Fatalf("window %+v", w)
	}
}

func TestGrokRefusesAPartialPanel(t *testing.T) {
	panel := fixture(t, "grok-usage.txt")
	for _, cut := range []string{"Weekly limit", "19%", "Resets:"} {
		if _, ok := ParseGrokUsage(strings.ReplaceAll(panel, cut, "")); ok {
			t.Errorf("parsed with %q missing", cut)
		}
	}
}

// fakeGrok writes a bash script standing in for the CLI. It logs everything it
// reads to input.log.
func fakeGrok(t *testing.T, dir, body string) string {
	t.Helper()
	abs := func(name string) string {
		p, err := filepath.Abs(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	script := fmt.Sprintf(`#!/bin/bash
stty raw -echo 2>/dev/null
draw() { printf '\033[2J\033[H'; sed 's/$/\r/' "$1"; }
key() { IFS= read -r -n1 -d '' c || exit 0; printf '%%s' "$c" >> %q/input.log; }
until_enter() { while key; do [ "$c" = $'\r' ] && return; done; }
TRUST=%q; COMPOSER=%q; USAGE=%q
env > %q/env.log
stty size > %q/size.log
pwd > %q/pwd.log
%s
`, dir, abs("grok-trust.txt"), abs("grok-composer.txt"), abs("grok-usage.txt"), dir, dir, dir, body)
	path := filepath.Join(dir, "grok")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func probeConfig(t *testing.T, cli string) (ProbeConfig, string) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".aiman/limits/grok-probe")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return ProbeConfig{Command: []string{cli}, Dir: dir, Home: home, PIDFile: filepath.Join(home, ".aiman/limits/grok-probe.pid"), Timeout: 8 * time.Second}, filepath.Dir(cli)
}

const fakeHappy = `draw "$TRUST"; until_enter; draw "$COMPOSER"; until_enter; draw "$USAGE"; sleep 60`

func TestProbeAnswersTrustThenReadsUsage(t *testing.T) {
	cfg, dir := probeConfig(t, fakeGrok(t, t.TempDir(), fakeHappy))
	r := RunGrokProbe(context.Background(), cfg)
	if r.State != StateOK || len(r.Windows) != 1 || r.Windows[0].UsedPercent != 19 {
		t.Fatalf("reading %+v", r)
	}
	input, err := os.ReadFile(filepath.Join(dir, "input.log"))
	if err != nil {
		t.Fatal(err)
	}
	if string(input) != "\r/usage\r" {
		t.Fatalf("keys sent = %q, want Enter then /usage Enter", input)
	}
}

func TestProbeSendsNoKeysToAnUnknownScreen(t *testing.T) {
	cfg, dir := probeConfig(t, fakeGrok(t, t.TempDir(), `printf 'Update available\r\n  Update now   enter\r\n  Skip  esc\r\n'; while key; do :; done; sleep 60`))
	cfg.Timeout = 2 * time.Second
	if r := RunGrokProbe(context.Background(), cfg); r.State != StateUnknown {
		t.Fatalf("reading %+v", r)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "input.log")); err == nil && len(raw) > 0 {
		t.Fatalf("sent %q to an unknown screen", raw)
	}
}

func TestProbeWaitsForALateComposer(t *testing.T) {
	cfg, dir := probeConfig(t, fakeGrok(t, t.TempDir(), `printf 'starting\r\n'; sleep 1; draw "$COMPOSER"; until_enter; draw "$USAGE"; sleep 60`))
	if r := RunGrokProbe(context.Background(), cfg); r.State != StateOK {
		t.Fatalf("reading %+v", r)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "input.log")); string(raw) != "/usage\r" {
		t.Fatalf("keys sent = %q", raw)
	}
}

func pidAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return err != nil && !os.IsNotExist(err)
	}
	f := strings.Fields(string(raw))
	return len(f) > 2 && f[2] != "Z"
}

func TestProbeKillsTheWholeTreeOnTimeout(t *testing.T) {
	dir := t.TempDir()
	cli := fakeGrok(t, dir, fmt.Sprintf(`sleep 300 & echo $! > %q/child.pid; echo $$ > %q/self.pid; while :; do sleep 1; done`, dir, dir))
	cfg, _ := probeConfig(t, cli)
	cfg.Timeout = 2 * time.Second
	start := time.Now()
	if r := RunGrokProbe(context.Background(), cfg); r.State != StateUnknown {
		t.Fatalf("reading %+v", r)
	}
	if took := time.Since(start); took > 8*time.Second {
		t.Fatalf("took %v past a 2s limit", took)
	}
	for _, f := range []string{"child.pid", "self.pid"} {
		raw, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
		for range 50 {
			if !pidAlive(pid) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if pidAlive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("%s still alive after the probe returned", f)
		}
	}
}

func TestProbeKillsTheTreeOnSuccessAndCancel(t *testing.T) {
	dir := t.TempDir()
	cli := fakeGrok(t, dir, fmt.Sprintf(`sleep 300 & echo $! > %q/child.pid; %s`, dir, fakeHappy))
	cfg, _ := probeConfig(t, cli)
	if r := RunGrokProbe(context.Background(), cfg); r.State != StateOK {
		t.Fatalf("reading %+v", r)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "child.pid"))
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
	t.Fatal("grandchild survived a successful probe")
}

func TestProbeGetsAMinimalEnvironmentAndALargeTerminal(t *testing.T) {
	t.Setenv("AIMAN_ID", "must-not-pass")
	t.Setenv("OPENROUTER_API_KEY", "sk-or-MUSTNOTPASS")
	t.Setenv("GH_TOKEN", "ghp_MUSTNOTPASS")
	cfg, dir := probeConfig(t, fakeGrok(t, t.TempDir(), fakeHappy))
	if r := RunGrokProbe(context.Background(), cfg); r.State != StateOK {
		t.Fatalf("reading %+v", r)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "env.log"))
	if err != nil {
		t.Fatal(err)
	}
	env := string(raw)
	for _, bad := range []string{"AIMAN_", "MUSTNOTPASS", "GH_TOKEN"} {
		if strings.Contains(env, bad) {
			t.Fatalf("environment carries %s", bad)
		}
	}
	if !strings.Contains(env, "HOME="+cfg.Home) || !strings.Contains(env, "TERM=xterm-256color") {
		t.Fatalf("environment missing HOME or TERM:\n%s", env)
	}
	size, _ := os.ReadFile(filepath.Join(dir, "size.log"))
	if !strings.Contains(string(size), "40 120") {
		t.Fatalf("terminal size %q, want 40 120", size)
	}
	pwd, _ := os.ReadFile(filepath.Join(dir, "pwd.log"))
	if strings.TrimSpace(string(pwd)) != cfg.Dir {
		t.Fatalf("cwd %q", pwd)
	}
}

func TestProbeRemovesOnlyItsOwnSessionFolder(t *testing.T) {
	cfg, _ := probeConfig(t, fakeGrok(t, t.TempDir(), fakeHappy))
	mine := filepath.Join(cfg.Home, ".grok/sessions", url.PathEscape(cfg.Dir))
	other := filepath.Join(cfg.Home, ".grok/sessions", url.PathEscape("/home/u/repos/real"))
	writeFile(t, filepath.Join(mine, "a/b.json"), "{}")
	writeFile(t, filepath.Join(other, "a/b.json"), "{}")
	if r := RunGrokProbe(context.Background(), cfg); r.State != StateOK {
		t.Fatalf("reading %+v", r)
	}
	if _, err := os.Stat(mine); !os.IsNotExist(err) {
		t.Fatalf("probe session folder left behind: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("another session's folder removed: %v", err)
	}
}

func TestReapStaleProbeChecksTheWorkingDirectory(t *testing.T) {
	dir, elsewhere := t.TempDir(), t.TempDir()
	start := func(cwd string) *exec.Cmd {
		c := exec.Command("sleep", "300")
		c.Dir = cwd
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
		return c
	}
	stale, innocent := start(dir), start(elsewhere)
	pidFile := filepath.Join(t.TempDir(), "probe.pid")
	writeFile(t, pidFile, strconv.Itoa(innocent.Process.Pid))
	ReapStaleProbe(pidFile, dir)
	if !pidAlive(innocent.Process.Pid) {
		t.Fatal("killed a process in another folder, as with a reused PID")
	}
	writeFile(t, pidFile, strconv.Itoa(stale.Process.Pid))
	ReapStaleProbe(pidFile, dir)
	done := make(chan error, 1)
	go func() { done <- stale.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stale probe not killed")
	}
}
