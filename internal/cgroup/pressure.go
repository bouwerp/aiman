package cgroup

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// HostPressureFile is the host-wide memory pressure file, read where serve
// runs in no user slice.
const HostPressureFile = "/proc/pressure/memory"

// Pressure is avg10 of the "some" and "full" lines of a memory.pressure file:
// the share of the last 10 seconds that at least one task, or every runnable
// task, was stalled on memory.
type Pressure struct{ SomeAvg10, FullAvg10 float64 }

// ReadPressure reads <dir>/memory.pressure. A file with no "full" line is not
// an error: FullAvg10 is 0.
func ReadPressure(dir string) (Pressure, error) {
	return ReadPressureFile(filepath.Join(dir, "memory.pressure"))
}

// ReadPressureFile reads a pressure file by path, such as HostPressureFile.
func ReadPressureFile(path string) (Pressure, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: a pressure file under /proc or a cgroup
	if err != nil {
		return Pressure{}, err
	}
	return parsePressureLines(string(raw))
}

// parsePressureLines parses
//
//	some avg10=0.70 avg60=0.18 avg300=1.81 total=8838764221
//	full avg10=0.00 avg60=0.00 avg300=0.00 total=0
//
// and fails only when there is no "some" line with an avg10.
func parsePressureLines(raw string) (Pressure, error) {
	var p Pressure
	some := false
	for _, line := range strings.Split(raw, "\n") {
		kind, rest, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || (kind != "some" && kind != "full") {
			continue
		}
		for _, field := range strings.Fields(rest) {
			v, ok := strings.CutPrefix(field, "avg10=")
			if !ok {
				continue
			}
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return Pressure{}, fmt.Errorf("pressure: %s avg10 %q: %w", kind, v, err)
			}
			if kind == "some" {
				p.SomeAvg10, some = f, true
			} else {
				p.FullAvg10 = f
			}
		}
	}
	if !some {
		return Pressure{}, fmt.Errorf("pressure: no some avg10 line")
	}
	return p, nil
}

// UserSliceOf is the user-<uid>.slice directory enclosing the cgroup path
// of a /proc/<pid>/cgroup file's "0::" line, or "" when the process runs in
// no user slice. It does not need a user systemd: a serve under the nohup
// supervisor sits in a session scope inside the same slice.
func UserSliceOf(procCgroup string) string {
	for _, line := range strings.Split(procCgroup, "\n") {
		path, ok := strings.CutPrefix(line, "0::")
		if !ok {
			continue
		}
		parts := strings.Split(strings.Trim(path, "/"), "/")
		for i, part := range parts {
			if strings.HasPrefix(part, "user-") && strings.HasSuffix(part, ".slice") {
				return filepath.Join(append([]string{fsRoot}, parts[:i+1]...)...)
			}
		}
	}
	return ""
}

// SliceOverHigh reports whether dir's memory.current is at or above its
// memory.high. A slice with no finite memory.high, or one that cannot be
// read, is not over.
func SliceOverHigh(dir string) bool {
	high, ok := readValue(dir, "memory.high")
	if !ok || high <= 0 || high == Infinity {
		return false
	}
	cur, ok := readValue(dir, "memory.current")
	return ok && cur >= high
}
