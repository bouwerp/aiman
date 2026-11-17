package config

import (
	"fmt"
	"strings"
)

// Harness network modes.
const (
	HarnessOffline = "offline"
	HarnessOnline  = "online"
	// DefaultHarnessCaptureBodyBytes is how much of each body a capture keeps.
	DefaultHarnessCaptureBodyBytes = 64 << 10
	// MaxHarnessCaptureBodyBytes caps harness.capture.body_bytes.
	MaxHarnessCaptureBodyBytes = 1 << 20
)

// HarnessCaptureConfig is harness.capture.
type HarnessCaptureConfig struct {
	// BodyBytes is how much of each request and response body a flow keeps
	// (64 KiB, at most 1 MiB).
	BodyBytes int `yaml:"body_bytes,omitempty"`
}

// NetworkMode is the harness runs' network: harness.network when set, else
// review.sandbox.network. Unset, none and offline are offline; any other
// value is online, where the app reaches the outside only through the edge
// proxy.
func (h HarnessConfig) NetworkMode(reviewNetwork string) string {
	v := strings.ToLower(strings.TrimSpace(h.Network))
	if v == "" {
		v = strings.ToLower(strings.TrimSpace(reviewNetwork))
	}
	switch v {
	case "", "none", HarnessOffline:
		return HarnessOffline
	}
	return HarnessOnline
}

// CaptureBodyBytes is harness.capture.body_bytes, the default when unset and
// clamped to the maximum, with a warning for a value it changed.
func (h HarnessConfig) CaptureBodyBytes() (int, []string) {
	v := h.Capture.BodyBytes
	switch {
	case v == 0:
		return DefaultHarnessCaptureBodyBytes, nil
	case v < 0:
		return DefaultHarnessCaptureBodyBytes, []string{fmt.Sprintf("harness.capture.body_bytes %d is negative; using %d", v, DefaultHarnessCaptureBodyBytes)}
	case v > MaxHarnessCaptureBodyBytes:
		return MaxHarnessCaptureBodyBytes, []string{fmt.Sprintf("harness.capture.body_bytes %d is above %d; using %d", v, MaxHarnessCaptureBodyBytes, MaxHarnessCaptureBodyBytes)}
	}
	return v, nil
}
