package design

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// execWaitDelay bounds how long a cancelled command's output pipes are
// drained, so a child that keeps them open cannot hold a publish.
const execWaitDelay = 5 * time.Second

// ExecRun runs a command in dir and returns its trimmed combined output. It
// is the Publisher's Run in production. git never prompts for credentials,
// and ctx ends the command.
func ExecRun(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.WaitDelay = execWaitDelay
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
