package config

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/cgroup"
	"gopkg.in/yaml.v3"
)

func TestResourcesDefaults(t *testing.T) {
	var r ResourcesConfig
	if !r.On() || !r.TmpdirOn() {
		t.Fatal("an unset resources block must be on, with tmpdir on")
	}
	l, warns := r.SessionLimits()
	want := cgroup.Limits{MemoryHigh: 6 << 30, MemoryMax: 10 << 30, MemorySwapMax: 1 << 30, CPUWeight: 100, IOWeight: 100, TasksMax: 4096}
	if l != want || len(warns) != 0 {
		t.Fatalf("defaults = %+v, warnings %v; want %+v", l, warns, want)
	}
	if v, w := r.SliceMemoryHigh(); v != 0 || len(w) != 0 {
		t.Fatalf("unset slice limit = %d, %v", v, w)
	}
}

func TestResourcesOverrides(t *testing.T) {
	var cfg Config
	src := `
resources:
  enabled: false
  session:
    memory_high: 12G
    memory_max: infinity
    memory_swap_max: 512M
    cpu_weight: 50
    tasks_max: 1000
  sessions_slice:
    memory_high: 20G
  tmpdir: false
`
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatal(err)
	}
	r := cfg.Resources
	if r.On() || r.TmpdirOn() {
		t.Fatal("enabled: false and tmpdir: false were not read")
	}
	l, warns := r.SessionLimits()
	want := cgroup.Limits{MemoryHigh: 12 << 30, MemoryMax: cgroup.Infinity, MemorySwapMax: 512 << 20, CPUWeight: 50, IOWeight: 100, TasksMax: 1000}
	if l != want || len(warns) != 0 {
		t.Fatalf("overrides = %+v, warnings %v; want %+v", l, warns, want)
	}
	if v, _ := r.SliceMemoryHigh(); v != 20<<30 {
		t.Fatalf("slice limit = %d", v)
	}
}

func TestResourcesInvalidValuesKeepDefaults(t *testing.T) {
	r := ResourcesConfig{
		Session:       SessionResources{MemoryHigh: "lots", MemoryMax: "2G", CPUWeight: 20000},
		SessionsSlice: SliceResources{MemoryHigh: "0"},
	}
	l, warns := r.SessionLimits()
	if l.MemoryHigh != 6<<30 || l.CPUWeight != 100 {
		t.Fatalf("invalid values did not keep defaults: %+v", l)
	}
	if l.MemoryMax != l.MemoryHigh {
		t.Fatalf("memory_max below memory_high was kept: %+v", l)
	}
	joined := strings.Join(warns, "\n")
	for _, key := range []string{"memory_high", "cpu_weight", "memory_max"} {
		if !strings.Contains(joined, key) {
			t.Errorf("no warning names %s:\n%s", key, joined)
		}
	}
	if v, w := r.SliceMemoryHigh(); v != 0 || len(w) != 1 {
		t.Fatalf("invalid slice limit = %d, %v", v, w)
	}
}

func TestMarshalServeConfigCarriesResources(t *testing.T) {
	off := false
	laptop := &Config{
		Remotes:   []Remote{{Host: "h1"}},
		Resources: ResourcesConfig{Enabled: &off, Session: SessionResources{MemoryHigh: "8G"}},
	}
	body, err := laptop.MarshalServeConfig("h1")
	if err != nil {
		t.Fatal(err)
	}
	var got Config
	if err := yaml.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Resources.On() || got.Resources.Session.MemoryHigh != "8G" {
		t.Fatalf("pushed resources = %+v\n%s", got.Resources, body)
	}
}
