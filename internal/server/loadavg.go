package server

import (
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// The state sweep runs every sweepPeriodLow while the 1-minute load average is
// below the CPU count and every sweepPeriodHigh above it, and one pass captures
// at most sweepCaptureBudget panes.
var sweepPeriodLow, sweepPeriodHigh = notifySweepEvery, 5 * time.Second

const sweepCaptureBudget = 16

// readLoadAvg reads the 1-minute load average from /proc/loadavg. Elsewhere
// than Linux it answers false. Tests swap it.
var readLoadAvg = func() (float64, bool) {
	if runtime.GOOS != "linux" {
		return 0, false
	}
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	return parseLoadAvg(string(b))
}

// parseLoadAvg reads the first field of a /proc/loadavg line.
func parseLoadAvg(s string) (float64, bool) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

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
