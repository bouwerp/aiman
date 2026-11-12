package hoststats

import (
	"math"
	"runtime"
	"strconv"
	"strings"
)

// procRoot is where Linux mounts /proc.
const procRoot = "/proc"

// readLoadAvgText reads loadavg under a /proc tree. It is the module's only
// reader of that file: the Sampler and ReadLoadAvg both go through it.
func readLoadAvgText(proc string) string { return readText(proc, "loadavg") }

// ParseLoadAvg reads the 1-minute load average, the first field of a loadavg
// line. An empty, unparseable, negative, NaN or infinite field is false.
func ParseLoadAvg(s string) (float64, bool) {
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

// ReadLoadAvg is the host's 1-minute load average. Elsewhere than Linux it is
// false.
func ReadLoadAvg() (float64, bool) {
	if runtime.GOOS != "linux" {
		return 0, false
	}
	return ParseLoadAvg(readLoadAvgText(procRoot))
}
