//go:build !windows

package ptyhold

import "syscall"

// detachAttr puts the holder in its own session so it survives the death of
// whatever spawned it — the whole point of the holder is outliving serve.
//
// Setsid alone is not sufficient when serve runs as a systemd unit: a new
// session escapes process-group signals, but cgroup membership is inherited
// across fork and cannot be shed, so the default KillMode=control-group takes
// the holders down with serve. The generated unit therefore sets
// KillMode=process (see internal/infra/remotesvc).
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
