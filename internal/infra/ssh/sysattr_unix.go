//go:build !windows

package ssh

import "syscall"

// sysProcAttrSetsid returns a SysProcAttr that places the child in its own
// session, isolating it from SIGHUP sent to our process group.
func sysProcAttrSetsid() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
