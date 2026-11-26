package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseFeedRetention(t *testing.T) {
	cases := map[string]time.Duration{"": 30 * 24 * time.Hour, "7d": 7 * 24 * time.Hour, "36h": 36 * time.Hour}
	for in, want := range cases {
		got, err := parseFeedRetention(in)
		if err != nil || got != want {
			t.Errorf("parseFeedRetention(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"0d", "-1h", "soon", "xd"} {
		if _, err := parseFeedRetention(bad); err == nil {
			t.Errorf("parseFeedRetention(%q) succeeded", bad)
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
	if err != nil || cfg.Feed.Enabled == nil || !*cfg.Feed.Enabled || cfg.Feed.Retention != "14d" {
		t.Fatalf("config = %+v, %v", cfg, err)
	}
}
