//go:build windows

package ssh

import "syscall"

// sysProcAttrSetsid is a no-op on Windows (Setsid is not supported).
func sysProcAttrSetsid() *syscall.SysProcAttr {
	return nil
}
