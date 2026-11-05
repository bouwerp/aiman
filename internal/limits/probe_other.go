//go:build !linux && !windows

package limits

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// setProbeDeathSignal does nothing: only Linux has Pdeathsig. ReapStaleProbe is
// the backstop here.
func setProbeDeathSignal(*syscall.SysProcAttr) {}

// probeProcessDir asks lsof, because these systems have no /proc/<pid>/cwd.
func probeProcessDir(pid int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	//nolint:gosec // G204: the only argument that varies is a number
	out, err := exec.CommandContext(ctx, "lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return "", fmt.Errorf("probe: lsof: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			return line[1:], nil
		}
	}
	return "", errors.New("probe: lsof reported no working directory")
}
