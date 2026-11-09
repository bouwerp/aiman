//go:build windows

package server

import "os"

// dumpSignals is empty on Windows, which has no SIGUSR1: only the auto dump
// runs there.
var dumpSignals []os.Signal
