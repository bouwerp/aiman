//go:build acceptance && unix

package selfupdate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/infra/remotesvc"
	"github.com/bouwerp/aiman/internal/selfupdate"
)

type runnerDirs struct {
	tmp, log, result string
}

func newRunnerDirs(t *testing.T) runnerDirs {
	t.Helper()
	root := t.TempDir()
	d := runnerDirs{
		tmp:    filepath.Join(root, "tmp"),
		log:    filepath.Join(root, "logs", "host-update.log"),
		result: filepath.Join(root, "host-update.result.json"),
	}
	for _, dir := range []string{d.tmp, filepath.Dir(d.log)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func TestP1RunnerStartsDetachedWrapper(t *testing.T) {
	d := newRunnerDirs(t)
	r := selfupdate.NewScriptRunner(d.tmp, d.log, d.result)
	if r.Command == nil {
		t.Fatal("NewScriptRunner left Command nil; it must default to exec.Command")
	}

	var gotName string
	var gotArgs []string
	var cmd *exec.Cmd
	r.Command = func(name string, args ...string) *exec.Cmd {
		gotName, gotArgs = name, args
		// Harmless stand-in: the real argv is captured, nothing is run.
		cmd = exec.Command("true")
		return cmd
	}

	const tag = "v0.35.0"
	script := "#!/bin/bash\necho 'installing aiman serve'\nexit 0\n"
	pid, err := r.Start(tag, script)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if cmd == nil || cmd.Process == nil {
		t.Fatal("Start did not start the command from Command")
	}
	_, _ = cmd.Process.Wait()
	if pid != cmd.Process.Pid {
		t.Fatalf("Start returned PID %d, the started process is %d", pid, cmd.Process.Pid)
	}

	wrapper := filepath.Join(d.tmp, "host-update-"+tag+".wrapper.sh")
	scriptPath := filepath.Join(d.tmp, "host-update-"+tag+".sh")
	if got := r.WrapperPath(tag); got != wrapper {
		t.Fatalf("WrapperPath(%q) = %q, want %q", tag, got, wrapper)
	}
	argv := append([]string{gotName}, gotArgs...)
	if want := []string{"nohup", "bash", wrapper}; !slices.Equal(argv, want) {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
	if want := r.Argv(tag); !slices.Equal(want, argv) {
		t.Fatalf("Argv(%q) = %q, Start used %q", tag, want, argv)
	}
	for _, a := range argv {
		if strings.Contains(a, "aiman serve") || strings.Contains(a, "\n") {
			t.Fatalf("script text leaked into argv: %q", argv)
		}
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatalf("SysProcAttr = %+v, want Setsid", cmd.SysProcAttr)
	}

	// The script itself is a 0600 file holding the script text.
	b, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	if string(b) != script {
		t.Fatalf("script file holds %q, want %q", b, script)
	}
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("script %s has mode %o, want 0600", scriptPath, perm)
	}

	// The wrapper runs the script, writes the Result through a temp file and a
	// rename, and has no set -e.
	wb, err := os.ReadFile(wrapper)
	if err != nil {
		t.Fatalf("read wrapper: %v", err)
	}
	w := string(wb)
	for _, want := range []string{scriptPath, d.result, d.result + ".tmp", "mv -f", tag} {
		if !strings.Contains(w, want) {
			t.Fatalf("wrapper lacks %q:\n%s", want, w)
		}
	}
	if strings.Contains(w, "set -e") {
		t.Fatalf("wrapper uses set -e, so a failed script would skip the Result:\n%s", w)
	}

	// stdout and stderr go to the log.
	for name, out := range map[string]any{"stdout": cmd.Stdout, "stderr": cmd.Stderr} {
		f, ok := out.(*os.File)
		if !ok || f.Name() != d.log {
			t.Fatalf("%s = %#v, want the log file %s", name, out, d.log)
		}
	}
}

// pkillPatterns returns every pkill -f pattern in s.
func pkillPatterns(t *testing.T, s string) []*regexp.Regexp {
	t.Helper()
	var out []*regexp.Regexp
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "pkill") {
			continue
		}
		start, end := strings.Index(line, "'"), strings.LastIndex(line, "'")
		if start < 0 || end <= start {
			t.Fatalf("cannot read pkill pattern from %q", line)
		}
		re, err := regexp.Compile(line[start+1 : end])
		if err != nil {
			t.Fatalf("pkill pattern in %q: %v", line, err)
		}
		out = append(out, re)
	}
	return out
}

func TestP1RunnerArgvSurvivesServePkill(t *testing.T) {
	const tag = "v0.35.0"
	r := selfupdate.NewScriptRunner("/home/x/.aiman/tmp", "/home/x/.aiman/logs/host-update.log", "/home/x/.aiman/host-update.result.json")
	cmdline := strings.Join(r.Argv(tag), " ")
	pats := pkillPatterns(t, remotesvc.UpdateScript(remotesvc.KindServe, tag)+"\n"+remotesvc.StopScript(remotesvc.KindServe))
	if len(pats) == 0 {
		t.Fatal("found no pkill patterns in the serve scripts")
	}
	for _, re := range pats {
		if re.MatchString(cmdline) {
			t.Fatalf("pkill pattern %q matches the runner's argv %q", re, cmdline)
		}
	}
}

func waitResult(t *testing.T, path string) selfupdate.Result {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if r, ok, _ := selfupdate.ReadResult(path); ok {
			return r
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no Result at %s within 15s", path)
	return selfupdate.Result{}
}

func needRealTools(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"nohup", "bash", "date", "mv"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
}

func TestP1RunnerWrapperWritesScriptExit(t *testing.T) {
	needRealTools(t)
	d := newRunnerDirs(t)
	r := selfupdate.NewScriptRunner(d.tmp, d.log, d.result)
	ppid := filepath.Join(t.TempDir(), "ppid")

	const tag = "v0.35.0"
	before := time.Now().Add(-time.Minute)
	// $PPID in the script is the wrapper, the process Start reports.
	pid, err := r.Start(tag, "echo running-update\necho $PPID > '"+ppid+"'\nexit 3\n")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("Start returned PID %d", pid)
	}
	res := waitResult(t, d.result)
	if res.Exit != 3 {
		t.Fatalf("Result.Exit = %d, want 3", res.Exit)
	}
	if res.Tag != tag {
		t.Fatalf("Result.Tag = %q, want %q", res.Tag, tag)
	}
	if res.At.Before(before) {
		t.Fatalf("Result.At = %v, want about now", res.At)
	}
	raw, err := os.ReadFile(ppid)
	if err != nil {
		t.Fatalf("script did not run: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != strconv.Itoa(pid) {
		t.Fatalf("Start returned PID %d, the wrapper ran as %s", pid, got)
	}
	if _, err := os.Stat(d.result + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp result left behind (err %v)", err)
	}
	logb, err := os.ReadFile(d.log)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(logb), "running-update") {
		t.Fatalf("script output missing from the log:\n%s", logb)
	}
}

func TestP1RunnerWrapperWritesZeroExit(t *testing.T) {
	needRealTools(t)
	d := newRunnerDirs(t)
	r := selfupdate.NewScriptRunner(d.tmp, d.log, d.result)
	if _, err := r.Start("v0.36.1", "true\n"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	res := waitResult(t, d.result)
	if res.Exit != 0 || res.Tag != "v0.36.1" {
		t.Fatalf("Result = %+v, want exit 0 tag v0.36.1", res)
	}
}

// procStat returns the process group and session of pid from /proc.
func procStat(t *testing.T, raw string) (pgrp, session string) {
	t.Helper()
	i := strings.LastIndex(raw, ")")
	if i < 0 {
		t.Fatalf("odd /proc stat %q", raw)
	}
	f := strings.Fields(raw[i+1:])
	if len(f) < 4 {
		t.Fatalf("odd /proc stat %q", raw)
	}
	return f[2], f[3]
}

// TestP1RunnerDetachesFromServe starts the script the way production does (real
// nohup, Setsid) and checks it runs in a session and process group of its
// own, so stopping serve's process or group leaves it running. It never runs
// pkill or systemctl: the host may run a real aiman serve.
func TestP1RunnerDetachesFromServe(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	needRealTools(t)
	self, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Skipf("no /proc: %v", err)
	}
	selfPgrp, selfSession := procStat(t, string(self))

	d := newRunnerDirs(t)
	out := filepath.Join(t.TempDir(), "stat")
	r := selfupdate.NewScriptRunner(d.tmp, d.log, d.result)
	if _, err := r.Start("v0.35.0", "cat /proc/$$/stat > '"+out+"'\n"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	res := waitResult(t, d.result)
	if res.Exit != 0 {
		t.Fatalf("script exit %d", res.Exit)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("script wrote no stat: %v", err)
	}
	pgrp, session := procStat(t, string(raw))
	if session == selfSession {
		t.Fatalf("script shares serve's session %s; Setsid did not detach it", session)
	}
	if pgrp == selfPgrp {
		t.Fatalf("script shares serve's process group %s", pgrp)
	}
}
