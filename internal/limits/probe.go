package limits

import (
	"context"
	"time"
)

// ProbeConfig describes one Grok probe. Stub until P3.
type ProbeConfig struct {
	// Command is the CLI and its arguments.
	Command []string
	// Dir is the fixed folder the CLI runs in.
	Dir string
	// Home is the HOME the CLI gets, and where its session folder is cleaned.
	Home string
	// PIDFile records the running probe.
	PIDFile string
	// Timeout is the hard limit for the whole run.
	Timeout time.Duration
}

// RunGrokProbe runs the probe once and always leaves nothing running.
func RunGrokProbe(_ context.Context, _ ProbeConfig) Reading {
	return Reading{Provider: "grok", State: StateUnknown, Reason: "not implemented"}
}

// ReapStaleProbe kills the PID in pidFile only if its working directory is dir.
func ReapStaleProbe(_, _ string) {}
