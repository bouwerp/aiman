//go:build unix

package server

import (
	"os"
	"syscall"
)

// dumpSignals are the signals that write a dump; SIGUSR1 on Unix.
var dumpSignals = []os.Signal{syscall.SIGUSR1}
