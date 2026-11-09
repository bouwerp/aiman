package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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
	pid, source, err := findServePID(runSystemctl, filepath.Join(dir, "serve.pid"))
	if err != nil {
		return err
	}
	if err := checkServePID(pid, source, os.Getpid(), runtime.GOOS, readCmdline); err != nil {
		return err
	}
	logs := filepath.Join(dir, "logs")
	since := time.Now()
	if err := signalServe(pid); err != nil {
		return fmt.Errorf("signal serve (pid %d): %w", pid, err)
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
	return fmt.Errorf("sent SIGUSR1 to serve (pid %d), but no dump appeared in %s within %s", pid, logs, hostDumpWait)
}

// readCmdline is /proc/<pid>/cmdline.
func readCmdline(pid int) ([]byte, error) {
	return os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
}

// checkServePID refuses to signal a pid that is not aiman serve. SIGUSR1
// terminates a process that does not handle it, and a stale serve.pid (after
// a crash or a reboot) can name a pid the kernel has since reused. On Linux
// the process's argv must be `<dir>/aiman serve ...`; elsewhere only our own
// pid is refused.
func checkServePID(pid int, source string, self int, goos string, cmdline func(int) ([]byte, error)) error {
	if pid == self {
		return fmt.Errorf("pid %d from %s is this process, not aiman serve", pid, source)
	}
	if goos != "linux" {
		return nil
	}
	raw, err := cmdline(pid)
	if err != nil {
		return fmt.Errorf("pid %d from %s is not running (%v); serve is not running or %s is stale", pid, source, err, source)
	}
	argv := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	if len(argv) < 2 || filepath.Base(argv[0]) != "aiman" || argv[1] != "serve" {
		return fmt.Errorf("pid %d from %s is not aiman serve (%q); not signalling it", pid, source, strings.Join(argv, " "))
	}
	return nil
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
	pid, _, err := findServePID(run, pidFile)
	return pid, err
}

// serveUnitSource names the MainPID lookup in messages.
const serveUnitSource = "the aiman-serve unit's MainPID"

// findServePID is serveMainPID plus where the pid came from.
func findServePID(run func(args ...string) (string, error), pidFile string) (int, string, error) {
	if out, err := run("systemctl", "--user", "show", "-p", "MainPID", "--value", "aiman-serve"); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(out)); err == nil && pid > 0 {
			return pid, serveUnitSource, nil
		}
	}
	raw, err := os.ReadFile(pidFile)
	if err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
			return pid, pidFile, nil
		}
	}
	return 0, "", errors.New("serve is not running: the aiman-serve user unit has no MainPID and " + pidFile + " holds no pid")
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
