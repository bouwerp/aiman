//go:build !windows

package gateway

import "syscall"

// detachAttr starts a child in a session of its own, so stopping the gateway's
// process group leaves it running.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
