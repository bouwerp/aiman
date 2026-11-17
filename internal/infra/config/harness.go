package config

import (
	"fmt"
	"time"

	"github.com/bouwerp/aiman/internal/cgroup"
)

// Defaults of the harness block.
const (
	DefaultHarnessMaxRuns     = 2
	DefaultHarnessIdleTimeout = 2 * time.Hour
	DefaultHarnessRunMemory   = "6G"
	DefaultHarnessSvcMemory   = "2G"
)

// HarnessConfig is the harness block: scenario runs serve starts for human
// testers. MarshalServeConfig pushes it.
type HarnessConfig struct {
	// Enabled turns the harness RPCs on; unset is on.
	Enabled *bool `yaml:"enabled,omitempty"`
	// AllowedImages pre-approves images (name:tag patterns, * within one
	// path element): a listed image whose scans are clean is approved with no
	// question. It never skips the scans. Unset is postgres:16, golang:* and
	// node:*.
	AllowedImages []string `yaml:"allowed_images,omitempty"`
	// ServiceMemory is each service's default memory limit (2G).
	ServiceMemory string `yaml:"service_memory,omitempty"`
	// RunMemory caps the memory sum of one run (6G), never above
	// resources.session.memory_high.
	RunMemory string `yaml:"run_memory,omitempty"`
	// MaxRuns caps the live runs on the host (2).
	MaxRuns int `yaml:"max_runs,omitempty"`
	// IdleTimeout takes down a run no RPC touched for this long (2h).
	IdleTimeout time.Duration `yaml:"idle_timeout,omitempty"`
	// Images configures image vetting.
	Images HarnessImagesConfig `yaml:"images,omitempty"`
	// DlvVersion and DlvModuleSum pin the Delve debug runs build on the
	// host: a version other than the one in code needs the h1: hash go.sum
	// lists for its module. Unset is the pin in code.
	DlvVersion   string `yaml:"dlv_version,omitempty"`
	DlvModuleSum string `yaml:"dlv_module_sum,omitempty"`
	// DebugTools overrides the pinned download of a debug tool (node,
	// js-debug): a version and its archive's sha256 for this host's
	// architecture. Unset is the pin in code.
	DebugTools map[string]HarnessToolPin `yaml:"debug_tools,omitempty"`
}

// HarnessToolPin is one harness.debug_tools entry.
type HarnessToolPin struct {
	Version string `yaml:"version,omitempty"`
	SHA256  string `yaml:"sha256,omitempty"`
}

// On reports whether the harness RPCs answer; unset is on.
func (h HarnessConfig) On() bool { return h.Enabled == nil || *h.Enabled }

// MaxRunsOrDefault is MaxRuns, or DefaultHarnessMaxRuns when unset.
func (h HarnessConfig) MaxRunsOrDefault() int {
	if h.MaxRuns > 0 {
		return h.MaxRuns
	}
	return DefaultHarnessMaxRuns
}

// IdleTimeoutOrDefault is IdleTimeout, or DefaultHarnessIdleTimeout when unset.
func (h HarnessConfig) IdleTimeoutOrDefault() time.Duration {
	if h.IdleTimeout > 0 {
		return h.IdleTimeout
	}
	return DefaultHarnessIdleTimeout
}

// ServiceMemoryBytes is service_memory in bytes; an unreadable or infinite
// value keeps the default and is named in the warning.
func (h HarnessConfig) ServiceMemoryBytes() (int64, []string) {
	return harnessSize("service_memory", h.ServiceMemory, DefaultHarnessSvcMemory)
}

// RunMemoryBytes is run_memory in bytes, clamped to sessionHigh
// (resources.session.memory_high) when that is set and lower.
func (h HarnessConfig) RunMemoryBytes(sessionHigh int64) (int64, []string) {
	v, warns := harnessSize("run_memory", h.RunMemory, DefaultHarnessRunMemory)
	if sessionHigh > 0 && sessionHigh != cgroup.Infinity && v > sessionHigh {
		warns = append(warns, fmt.Sprintf("harness.run_memory %s is above resources.session.memory_high %s; using %s",
			cgroup.FormatSize(v), cgroup.FormatSize(sessionHigh), cgroup.FormatSize(sessionHigh)))
		v = sessionHigh
	}
	return v, warns
}

func harnessSize(key, val, def string) (int64, []string) {
	d, _ := cgroup.ParseSize(def)
	if val == "" {
		return d, nil
	}
	v, err := cgroup.ParseSize(val)
	if err != nil || v <= 0 || v == cgroup.Infinity {
		return d, []string{fmt.Sprintf("harness.%s %q is not a finite size; using %s", key, val, def)}
	}
	return v, nil
}
