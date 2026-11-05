package limits

import "context"

// RunGrokProbe reports the source as not applying: the probe needs a Unix
// terminal.
func RunGrokProbe(context.Context, ProbeConfig) Reading {
	return Reading{Provider: grokProvider, State: StateUnavailable, Source: "probe"}
}

// ReapStaleProbe has nothing to reap where the probe never runs.
func ReapStaleProbe(_, _ string) {}
