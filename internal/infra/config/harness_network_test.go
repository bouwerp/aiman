package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestHarnessNetworkMode(t *testing.T) {
	for _, tc := range []struct{ harness, review, want string }{
		{"", "", HarnessOffline},
		{"", "none", HarnessOffline},
		{"", "bridge", HarnessOnline},
		{"offline", "bridge", HarnessOffline},
		{"none", "bridge", HarnessOffline},
		{"online", "", HarnessOnline},
		{" Online ", "none", HarnessOnline},
	} {
		if got := (HarnessConfig{Network: tc.harness}).NetworkMode(tc.review); got != tc.want {
			t.Errorf("harness.network %q, review %q = %s, want %s", tc.harness, tc.review, got, tc.want)
		}
	}
}

func TestHarnessCaptureBodyBytes(t *testing.T) {
	for _, tc := range []struct {
		in, want int
		warns    bool
	}{
		{0, DefaultHarnessCaptureBodyBytes, false},
		{1024, 1024, false},
		{-1, DefaultHarnessCaptureBodyBytes, true},
		{8 << 20, MaxHarnessCaptureBodyBytes, true},
	} {
		got, warns := (HarnessConfig{Capture: HarnessCaptureConfig{BodyBytes: tc.in}}).CaptureBodyBytes()
		if got != tc.want || (len(warns) > 0) != tc.warns {
			t.Errorf("body_bytes %d = %d %v", tc.in, got, warns)
		}
	}
	var h HarnessConfig
	if err := yaml.Unmarshal([]byte("network: online\ncapture: {body_bytes: 2048}\n"), &h); err != nil {
		t.Fatal(err)
	}
	if h.Network != "online" || h.Capture.BodyBytes != 2048 {
		t.Fatalf("parsed %+v", h)
	}
}
