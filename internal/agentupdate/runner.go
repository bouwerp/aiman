package agentupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// UpdateTimeout bounds one update command.
const UpdateTimeout = 10 * time.Minute

// ErrPrivileged is returned for a command that would escalate privileges.
// Updates never run through sudo.
var ErrPrivileged = errors.New("refusing to run an update with elevated privileges")

var needsRootRe = regexp.MustCompile(`(?i)\bEACCES\b|permission denied`)

// Result is the outcome of one update command.
type Result struct {
	Agent     string
	Command   string
	Output    string
	OK        bool
	NeedsRoot bool
	Err       error
	Started   time.Time
	Finished  time.Time
}

// Runner applies updates one at a time and appends each run to LogPath.
type Runner struct {
	Exec    Executor
	LogPath string
	Timeout time.Duration
	Now     func() time.Time

	mu sync.Mutex
}

// NewRunner returns a Runner with the default timeout.
func NewRunner(exec Executor, logPath string) *Runner {
	return &Runner{Exec: exec, LogPath: logPath, Timeout: UpdateTimeout}
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Run executes c for agent. Concurrent calls queue behind the one in flight.
// A permission failure becomes "needs root: <command>".
func (r *Runner) Run(ctx context.Context, agent string, c Command) Result {
	res := Result{Agent: agent, Command: c.String()}
	if len(c.Argv) == 0 {
		res.Err = errors.New("empty update command")
		return res
	}
	if privileged(c.Argv[0]) {
		res.Err = fmt.Errorf("%w: %s", ErrPrivileged, res.Command)
		return res
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = UpdateTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	res.Started = r.now()
	out, err := r.Exec.Run(ctx, c)
	res.Finished = r.now()
	res.Output = out
	switch {
	case err == nil:
		res.OK = true
	case needsRootRe.MatchString(out) || needsRootRe.MatchString(err.Error()):
		res.NeedsRoot = true
		res.Err = fmt.Errorf("needs root: %s", res.Command)
	default:
		res.Err = fmt.Errorf("%s: %w", res.Command, err)
	}
	// A log that cannot be written does not change the update's outcome.
	_ = r.appendLog(res)
	return res
}

func privileged(argv0 string) bool {
	switch filepath.Base(argv0) {
	case "sudo", "doas", "su", "pkexec":
		return true
	default:
		return false
	}
}

func (r *Runner) appendLog(res Result) error {
	if r.LogPath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.LogPath), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(r.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	outcome := "ok"
	if res.Err != nil {
		outcome = "failed: " + res.Err.Error()
	}
	_, werr := fmt.Fprintf(f, "=== %s %s: %s\n%s\n=== %s (%s)\n",
		res.Started.UTC().Format(time.RFC3339), res.Agent, res.Command,
		strings.TrimRight(res.Output, "\r\n"), outcome, res.Finished.Sub(res.Started).Round(time.Millisecond))
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}
