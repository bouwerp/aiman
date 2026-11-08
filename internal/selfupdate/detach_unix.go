//go:build !windows

package selfupdate

import "syscall"

// detachAttr starts the wrapper in a session of its own, the PTY holder's
// detach (internal/ptyhold): stopping serve's process or process group leaves
// it running. Cgroup membership is inherited; the serve unit's
// KillMode=process is what spares the rest of the cgroup.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// Alive reports whether a PID is running; tests replace it.
var Alive = func(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}
