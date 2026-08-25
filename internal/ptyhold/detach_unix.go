//go:build !windows

package ptyhold

import "syscall"

// detachAttr puts the holder in its own session so it survives the death of
// whatever spawned it — the whole point of the holder is outliving serve.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
