package server

import (
	"runtime"
	"time"

	"github.com/bouwerp/aiman/internal/hoststats"
)

// The state sweep runs every sweepPeriodLow while the 1-minute load average is
// below the CPU count and every sweepPeriodHigh above it, and one pass captures
// at most sweepCaptureBudget panes.
var sweepPeriodLow, sweepPeriodHigh = notifySweepEvery, 5 * time.Second

const sweepCaptureBudget = 16

// readLoadAvg is the 1-minute load average (hoststats.ReadLoadAvg). Tests
// swap it.
var readLoadAvg = hoststats.ReadLoadAvg

// sweepPeriod is sweepPeriodHigh when the load average is known and at or above
// the CPU count, else sweepPeriodLow.
func sweepPeriod(load float64, ok bool, ncpu int) time.Duration {
	if ok && load >= float64(ncpu) {
		return sweepPeriodHigh
	}
	return sweepPeriodLow
}

// currentSweepPeriod is the period for the host's load right now.
func currentSweepPeriod() time.Duration {
	load, ok := readLoadAvg()
	return sweepPeriod(load, ok, runtime.NumCPU())
}
