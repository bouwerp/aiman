//go:build unix

package cigate

import (
	"os/exec"
	"syscall"
)

// setProcessGroup runs the command in its own process group and kills the
// whole group on cancel, so a timed-out test runner takes its children along.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
