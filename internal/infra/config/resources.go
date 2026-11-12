package config

import (
	"fmt"

	"github.com/bouwerp/aiman/internal/cgroup"
)

// Per-session defaults of the resources block.
const (
	DefaultSessionMemoryHigh    = "6G"
	DefaultSessionMemoryMax     = "10G"
	DefaultSessionMemorySwapMax = "1G"
	DefaultSessionCPUWeight     = 100
	DefaultSessionIOWeight      = 100
	DefaultSessionTasksMax      = 4096
)

// ResourcesConfig is the resources block: the cgroup limits serve gives each
// session's terminal on a host with a user systemd, and the per-session
// scratch directory. On any other host the limits do nothing.
type ResourcesConfig struct {
	// Enabled turns per-session scopes on; unset is on. False spawns
	// holders in serve's own cgroup.
	Enabled *bool `yaml:"enabled,omitempty"`
	// Session is the limit set of one session's scope.
	Session SessionResources `yaml:"session,omitempty"`
	// SessionsSlice bounds every session scope together.
	SessionsSlice SliceResources `yaml:"sessions_slice,omitempty"`
	// Tmpdir gives each session TMPDIR=~/.aiman/tmp/<id>; unset is on.
	Tmpdir *bool `yaml:"tmpdir,omitempty"`
}

// SessionResources is resources.session. Sizes are numbers with a K, M, G or
// T suffix, or "infinity"; an empty field keeps its default.
type SessionResources struct {
	MemoryHigh    string `yaml:"memory_high,omitempty"`
	MemoryMax     string `yaml:"memory_max,omitempty"`
	MemorySwapMax string `yaml:"memory_swap_max,omitempty"`
	CPUWeight     int    `yaml:"cpu_weight,omitempty"`
	IOWeight      int    `yaml:"io_weight,omitempty"`
	TasksMax      int    `yaml:"tasks_max,omitempty"`
}

// SliceResources is resources.sessions_slice.
type SliceResources struct {
	// MemoryHigh is the aggregate soft limit of all session scopes. Empty
	// lets serve derive it from the user slice's own limit.
	MemoryHigh string `yaml:"memory_high,omitempty"`
}

// On reports whether sessions get their own scope.
func (r ResourcesConfig) On() bool { return r.Enabled == nil || *r.Enabled }

// TmpdirOn reports whether sessions get their own TMPDIR.
func (r ResourcesConfig) TmpdirOn() bool { return r.Tmpdir == nil || *r.Tmpdir }

// SessionLimits is the limit set for one session scope: the configured
// values over the defaults. An unreadable size or an out-of-range number
// keeps its default and is named in the returned warnings.
func (r ResourcesConfig) SessionLimits() (cgroup.Limits, []string) {
	var warns []string
	size := func(key, val, def string) int64 {
		if v, err := cgroup.ParseSize(val); err != nil {
			warns = append(warns, fmt.Sprintf("resources.session.%s: %v; using %s", key, err, def))
		} else if v != 0 {
			return v
		}
		v, _ := cgroup.ParseSize(def)
		return v
	}
	num := func(key string, val, def, lo, hi int) int {
		switch {
		case val == 0:
			return def
		case val < lo || val > hi:
			warns = append(warns, fmt.Sprintf("resources.session.%s: %d is outside %d-%d; using %d", key, val, lo, hi, def))
			return def
		}
		return val
	}
	s := r.Session
	l := cgroup.Limits{
		MemoryHigh:    size("memory_high", s.MemoryHigh, DefaultSessionMemoryHigh),
		MemoryMax:     size("memory_max", s.MemoryMax, DefaultSessionMemoryMax),
		MemorySwapMax: size("memory_swap_max", s.MemorySwapMax, DefaultSessionMemorySwapMax),
		CPUWeight:     num("cpu_weight", s.CPUWeight, DefaultSessionCPUWeight, 1, 10000),
		IOWeight:      num("io_weight", s.IOWeight, DefaultSessionIOWeight, 1, 10000),
		TasksMax:      num("tasks_max", s.TasksMax, DefaultSessionTasksMax, 1, 1<<22),
	}
	if l.MemoryHigh > 0 && l.MemoryMax > 0 && l.MemoryMax < l.MemoryHigh {
		warns = append(warns, fmt.Sprintf("resources.session.memory_max %s is below memory_high %s; using memory_high",
			cgroup.FormatSize(l.MemoryMax), cgroup.FormatSize(l.MemoryHigh)))
		l.MemoryMax = l.MemoryHigh
	}
	return l, warns
}

// SliceMemoryHigh is resources.sessions_slice.memory_high in bytes: 0 when
// unset (serve derives it), with a warning when the value cannot be read.
func (r ResourcesConfig) SliceMemoryHigh() (int64, []string) {
	v, err := cgroup.ParseSize(r.SessionsSlice.MemoryHigh)
	if err != nil {
		return 0, []string{fmt.Sprintf("resources.sessions_slice.memory_high: %v; deriving it from the user slice", err)}
	}
	return v, nil
}
