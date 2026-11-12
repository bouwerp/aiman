// Package cgroup places each session's PTY holder in its own transient
// systemd user scope, so a session that exhausts its memory budget is
// throttled alone, and reads the scope's usage back from the cgroup files.
// It needs Linux with a user systemd; everywhere else Probe reports
// unavailable and callers spawn holders plainly.
package cgroup

import (
	"fmt"
	"strconv"
	"strings"
)

// Infinity is the size that means "no limit" (systemd's "infinity").
const Infinity int64 = -1

// Limits are one scope's resource-control properties. A zero field is left
// unset, so the scope inherits systemd's default for it.
type Limits struct {
	MemoryHigh    int64 `json:"memory_high,omitempty"`
	MemoryMax     int64 `json:"memory_max,omitempty"`
	MemorySwapMax int64 `json:"memory_swap_max,omitempty"`
	CPUWeight     int   `json:"cpu_weight,omitempty"`
	IOWeight      int   `json:"io_weight,omitempty"`
	TasksMax      int   `json:"tasks_max,omitempty"`
}

// Merge returns l with every non-zero field of over applied on top.
func (l Limits) Merge(over Limits) Limits {
	if over.MemoryHigh != 0 {
		l.MemoryHigh = over.MemoryHigh
	}
	if over.MemoryMax != 0 {
		l.MemoryMax = over.MemoryMax
	}
	if over.MemorySwapMax != 0 {
		l.MemorySwapMax = over.MemorySwapMax
	}
	if over.CPUWeight != 0 {
		l.CPUWeight = over.CPUWeight
	}
	if over.IOWeight != 0 {
		l.IOWeight = over.IOWeight
	}
	if over.TasksMax != 0 {
		l.TasksMax = over.TasksMax
	}
	// A raised soft limit must stay below the hard one, or the kernel kills
	// before it throttles.
	if l.MemoryHigh > 0 && l.MemoryMax > 0 && l.MemoryMax < l.MemoryHigh {
		l.MemoryMax = l.MemoryHigh
	}
	return l
}

// Properties are the systemd property assignments for l, in a fixed order.
func (l Limits) Properties() []string {
	var out []string
	size := func(name string, v int64) {
		if v != 0 {
			out = append(out, name+"="+sizeProperty(v))
		}
	}
	num := func(name string, v int) {
		if v > 0 {
			out = append(out, name+"="+strconv.Itoa(v))
		}
	}
	size("MemoryHigh", l.MemoryHigh)
	size("MemoryMax", l.MemoryMax)
	size("MemorySwapMax", l.MemorySwapMax)
	num("CPUWeight", l.CPUWeight)
	num("IOWeight", l.IOWeight)
	num("TasksMax", l.TasksMax)
	return out
}

func sizeProperty(v int64) string {
	if v < 0 {
		return "infinity"
	}
	return strconv.FormatInt(v, 10)
}

// ParseSize reads a byte size: a plain number of bytes, a number with a
// binary suffix K, M, G or T (optionally followed by B or iB, any case, as in
// "6G", "512m" or "1.5GiB"), or "infinity". Zero and negative sizes are
// refused; an empty string is 0 with no error (unset).
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	low := strings.ToLower(s)
	if low == "infinity" || low == "max" {
		return Infinity, nil
	}
	low = strings.TrimSuffix(strings.TrimSuffix(low, "b"), "i")
	mult := int64(1)
	if n := len(low); n > 0 {
		switch low[n-1] {
		case 'k':
			mult = 1 << 10
		case 'm':
			mult = 1 << 20
		case 'g':
			mult = 1 << 30
		case 't':
			mult = 1 << 40
		}
		if mult > 1 {
			low = low[:n-1]
		}
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(low), 64)
	if err != nil || f <= 0 || f*float64(mult) >= 1<<62 {
		return 0, fmt.Errorf("invalid size %q: want a number with K, M, G or T, or infinity", s)
	}
	return int64(f * float64(mult)), nil
}

// FormatSize renders a byte count for people: "6G", "512M", "1.5G",
// "infinity" for a negative value and "0" for zero.
func FormatSize(v int64) string {
	if v < 0 {
		return "infinity"
	}
	units := []struct {
		suffix string
		size   int64
	}{{"T", 1 << 40}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}}
	for _, u := range units {
		if v >= u.size {
			s := strconv.FormatFloat(float64(v)/float64(u.size), 'f', 1, 64)
			return strings.TrimSuffix(s, ".0") + u.suffix
		}
	}
	return strconv.FormatInt(v, 10)
}
