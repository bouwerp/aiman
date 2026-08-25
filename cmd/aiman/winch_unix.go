//go:build !windows

package main

import (
	"os"
	"syscall"
)

// resizeSignals are the signals that mean "the terminal changed size".
// Windows has no SIGWINCH, so this is per-platform.
func resizeSignals() []os.Signal {
	return []os.Signal{syscall.SIGWINCH}
}
