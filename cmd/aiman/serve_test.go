package main

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/usage"
)

func TestUsagePricesConfirmed(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Usage: config.UsageConfig{Prices: map[string]config.ModelPrice{
		"custom-1": {Input: 1.5, Output: 2, CacheRead: 0.1, CacheWrite5m: 3, CacheWrite1h: 4},
	}}}
	got := usagePrices(cfg)
	p, ok := got["custom-1"]
	if !ok || !p.Confirmed || p.Estimated || p.Input != 1.5 || p.Output != 2 || p.CacheRead != 0.1 || p.CacheWrite5m != 3 || p.CacheWrite1h != 4 {
		t.Fatalf("%+v", got)
	}
	if usagePrices(nil) != nil {
		t.Fatalf("nil config: %+v", usagePrices(nil))
	}
	if usagePrices(&config.Config{}) != nil {
		t.Fatal("empty prices should not replace defaults")
	}
	merged := usage.DefaultPrices().Merge(got)
	if !merged["custom-1"].Confirmed || merged["claude-haiku-4-5"].Input != 1 {
		t.Fatalf("merge = %+v", merged["custom-1"])
	}
}

func TestServeWantsHelp(t *testing.T) {
	t.Parallel()
	if !serveWantsHelp([]string{"--help"}) || !serveWantsHelp([]string{"-h"}) {
		t.Fatal("expected help flags")
	}
	if serveWantsHelp(nil) || serveWantsHelp([]string{"--foreground"}) {
		t.Fatal("non-help args")
	}
}

func TestPrintServeUsageTellsOperatorTheTUIPath(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	printServeUsage(&b)
	out := b.String()
	for _, want := range []string{
		"agent API",
		"m  →  Agent API",
		"i  install/enable",
		"~/.aiman/aiman.sock",
		"Do not run this on your laptop",
		"native-session hooks",
		"installs mosh when mosh-server is missing",
		"usage.get",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("usage missing %q:\n%s", want, out)
		}
	}
}
