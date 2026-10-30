package review

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// execWaitDelay bounds how long a cancelled command may hold its pipes.
const execWaitDelay = 5 * time.Second

// ExecRun runs name with args and returns its stdout. Stderr goes into the
// error only, so a gh warning never corrupts the JSON a caller parses.
func ExecRun(ctx context.Context, name string, args ...string) (string, error) {
	return ExecRunInput(ctx, "", name, args...)
}

// ExecRunInput is ExecRun with input on stdin.
func ExecRunInput(ctx context.Context, input, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(input)
	cmd.WaitDelay = execWaitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
