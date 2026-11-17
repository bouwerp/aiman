package config

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestHarnessConfigDefaults(t *testing.T) {
	var h HarnessConfig
	if !h.On() || h.MaxRunsOrDefault() != 2 || h.IdleTimeoutOrDefault() != 2*time.Hour {
		t.Fatalf("defaults: on %v, max %d, idle %s", h.On(), h.MaxRunsOrDefault(), h.IdleTimeoutOrDefault())
	}
	if v, w := h.ServiceMemoryBytes(); v != 2<<30 || w != nil {
		t.Fatalf("service memory %d %v", v, w)
	}
	if v, w := h.RunMemoryBytes(0); v != 6<<30 || w != nil {
		t.Fatalf("run memory %d %v", v, w)
	}
}

func TestHarnessRunMemoryClampsToSessionHigh(t *testing.T) {
	h := HarnessConfig{RunMemory: "8G"}
	v, w := h.RunMemoryBytes(6 << 30)
	if v != 6<<30 || len(w) != 1 || !strings.Contains(w[0], "memory_high") {
		t.Fatalf("clamp: %d %v", v, w)
	}
	h = HarnessConfig{RunMemory: "lots"}
	if v, w := h.RunMemoryBytes(0); v != 6<<30 || len(w) != 1 {
		t.Fatalf("bad size: %d %v", v, w)
	}
}

func TestMarshalServeConfigCarriesHarness(t *testing.T) {
	off := false
	c := &Config{
		Harness: HarnessConfig{Enabled: &off, AllowedImages: []string{"redis:7"}, MaxRuns: 3},
		Repos: map[string]RepoBootstrap{"o/r": {Harness: map[string]any{
			"version": 1, "app": map[string]any{"run": "serve"},
		}}},
	}
	raw, err := c.MarshalServeConfig("h")
	if err != nil {
		t.Fatal(err)
	}
	var got ServeConfig
	if err := yaml.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Harness.On() || got.Harness.MaxRuns != 3 || len(got.Harness.AllowedImages) != 1 {
		t.Fatalf("harness = %+v", got.Harness)
	}
	if got.Repos["o/r"].Harness["version"] != 1 {
		t.Fatalf("repos.o/r.harness = %v", got.Repos["o/r"].Harness)
	}
}
