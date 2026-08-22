package main

import (
	"strings"
	"testing"
)

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
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("usage missing %q:\n%s", want, out)
		}
	}
}
