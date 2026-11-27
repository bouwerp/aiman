package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/bouwerp/aiman/internal/feedcollect"
)

func TestFeedCollectRetentionFromConfig(t *testing.T) {
	cases := map[string]time.Duration{"": 30 * 24 * time.Hour, "7d": 7 * 24 * time.Hour, "36h": 36 * time.Hour, "soon": 30 * 24 * time.Hour}
	for in, want := range cases {
		var cfg feedCollectConfig
		cfg.Feed.Retention = in
		if got := cfg.Feed.RetentionOrDefault(); got != want {
			t.Errorf("retention %q = %v; want %v", in, got, want)
		}
	}
}

func TestReadFeedCollectConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg, err := readFeedCollectConfig(path)
	if err != nil || cfg.Feed.Enabled != nil {
		t.Fatalf("missing file: %+v, %v", cfg, err)
	}
	if err := os.WriteFile(path, []byte("remotes: []\nfeed:\n  enabled: true\n  retention: 14d\n  slack:\n    exclude: [C1]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = readFeedCollectConfig(path)
	if err != nil || !cfg.Feed.EnabledOrDefault() || cfg.Feed.Retention != "14d" {
		t.Fatalf("config = %+v, %v", cfg, err)
	}
}

func TestFeedSourcesSkipsSwitchedOff(t *testing.T) {
	var cfg feedCollectConfig
	if err := yaml.Unmarshal([]byte("feed:\n  enabled: true\n  sessions:\n    enabled: false\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	built := map[string]bool{}
	mk := func(name string) func() feedcollect.Source {
		return func() feedcollect.Source { built[name] = true; return feedcollect.NewGitHubSource(nil) }
	}
	srcs := feedSources(cfg.Feed, mk("aiman"), mk("github"), func() feedcollect.Source { built["slack"] = true; return nil })
	if built["aiman"] || !built["github"] || !built["slack"] || len(srcs) != 1 {
		t.Fatalf("built %v, %d sources; want github and slack built, aiman not, one source", built, len(srcs))
	}
	if off := cfg.Feed.OffSources(); len(off) != 1 || off[0] != "aiman" {
		t.Errorf("off = %v", off)
	}
}
