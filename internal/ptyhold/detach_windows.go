//go:build windows

package ptyhold

import "syscall"

// detachAttr has no Setsid equivalent on Windows. The PTY runtime is a
// Unix-only backend; this exists so the package still cross-compiles.
func detachAttr() *syscall.SysProcAttr {
	return nil
}
