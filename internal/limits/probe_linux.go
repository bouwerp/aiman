package limits

import (
	"fmt"
	"os"
	"syscall"
)

// setProbeDeathSignal kills the CLI if the thread that created it dies, which
// covers a serve killed outright. The unit's KillMode=process would spare it.
func setProbeDeathSignal(attrs *syscall.SysProcAttr) {
	attrs.Pdeathsig = syscall.SIGKILL
}

func probeProcessDir(pid int) (string, error) {
	return os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
}
