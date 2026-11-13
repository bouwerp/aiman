//go:build windows

package gateway

import "syscall"

// detachAttr has nothing to set on Windows, where serve runs no units.
func detachAttr() *syscall.SysProcAttr { return nil }
