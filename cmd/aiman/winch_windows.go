//go:build windows

package main

import "os"

// resizeSignals is empty on Windows: there is no SIGWINCH, so an attached
// terminal simply does not propagate live resizes there.
func resizeSignals() []os.Signal {
	return nil
}
