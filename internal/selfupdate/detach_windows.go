//go:build windows

package selfupdate

import (
	"os"
	"syscall"
)

// detachAttr has nothing to set on Windows, where serve does not update itself.
func detachAttr() *syscall.SysProcAttr { return nil }

// Alive reports whether a PID is running; tests replace it.
var Alive = func(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
