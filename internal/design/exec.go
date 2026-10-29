package design

import (
	"context"
	"os/exec"
	"strings"
)

// ExecRun runs a command in dir and returns its trimmed combined output. It
// is the Publisher's Run in production.
func ExecRun(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
