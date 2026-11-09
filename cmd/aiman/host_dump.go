package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bouwerp/aiman/internal/infra/config"
)

// hostDumpWait is how long `aiman host dump` waits for serve's dump file.
const hostDumpWait = 5 * time.Second

// runHostDump is `aiman host dump`: SIGUSR1 to serve, then the dump's path.
func runHostDump(out io.Writer) error {
	dir, err := config.GetDir()
	if err != nil {
		return err
	}
	pid, err := serveMainPID(runSystemctl, filepath.Join(dir, "serve.pid"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	logs := filepath.Join(dir, "logs")
	since := time.Now()
	if err := syscall.Kill(pid, syscall.SIGUSR1); err != nil {
		err = fmt.Errorf("signal serve (pid %d): %w", pid, err)
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	for deadline := time.Now().Add(hostDumpWait); ; {
		if path, ok := newestDumpSince(logs, since); ok {
			fmt.Fprintln(out, path)
			return nil
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	err = fmt.Errorf("sent SIGUSR1 to serve (pid %d), but no dump appeared in %s within %s", pid, logs, hostDumpWait)
	fmt.Fprintln(os.Stderr, err)
	return err
}

// runSystemctl runs systemctl (args[0] names it) and returns its stdout. User
// systemd needs XDG_RUNTIME_DIR, which a non-login shell may lack.
func runSystemctl(args ...string) (string, error) {
	if len(args) == 0 || args[0] != "systemctl" {
		return "", errors.New("runSystemctl runs only systemctl")
	}
	cmd := exec.Command("systemctl", args[1:]...) //nolint:gosec // G204: serveMainPID passes a fixed argument list
	cmd.Env = os.Environ()
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", os.Getuid()))
	}
	b, err := cmd.Output()
	return string(b), err
}

// serveMainPID is the aiman-serve unit's MainPID, else the pid in pidFile.
func serveMainPID(run func(args ...string) (string, error), pidFile string) (int, error) {
	if out, err := run("systemctl", "--user", "show", "-p", "MainPID", "--value", "aiman-serve"); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(out)); err == nil && pid > 0 {
			return pid, nil
		}
	}
	raw, err := os.ReadFile(pidFile)
	if err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
			return pid, nil
		}
	}
	return 0, errors.New("serve is not running: the aiman-serve user unit has no MainPID and " + pidFile + " holds no pid")
}

// mtimeSlack absorbs file times that run behind the clock: the kernel stamps
// them from a coarse clock, and some filesystems keep whole seconds.
const mtimeSlack = time.Second

// newestDumpSince is the newest serve-dump-*.txt in dir modified at or after
// since, within mtimeSlack.
func newestDumpSince(dir string, since time.Time) (string, bool) {
	since = since.Add(-mtimeSlack)
	matches, _ := filepath.Glob(filepath.Join(dir, "serve-dump-*.txt"))
	var best string
	var bestAt time.Time
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil || !info.Mode().IsRegular() || info.ModTime().Before(since) {
			continue
		}
		if best == "" || info.ModTime().After(bestAt) {
			best, bestAt = m, info.ModTime()
		}
	}
	return best, best != ""
}
