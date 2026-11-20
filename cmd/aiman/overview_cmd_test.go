package main

import (
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/server"
)

func TestFormatOverview(t *testing.T) {
	at := time.Date(2026, 11, 20, 7, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		run  server.OverviewRun
		want string
	}{
		{server.OverviewRun{ID: "r1", State: server.OverviewDone, Agent: "codex", StartedAt: at, Markdown: "## In progress\n- a: b"}, "## In progress\n- a: b\n"},
		{server.OverviewRun{ID: "r2", State: server.OverviewFailed, StartedAt: at, Error: "serve restarted"}, "failed: serve restarted"},
		{server.OverviewRun{ID: "r3", State: server.OverviewRunning, Agent: "claude", StartedAt: at, Sessions: 4}, "running with claude (4 sessions)"},
	} {
		var b strings.Builder
		if err := formatOverview(&b, tc.run); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(b.String(), tc.want) || !strings.HasPrefix(b.String(), "overview "+tc.run.ID) {
			t.Errorf("%s: %q", tc.run.ID, b.String())
		}
	}
}

func TestRunOverviewRefusesUnknownSubcommands(t *testing.T) {
	if err := runOverview([]string{"bogus"}, &strings.Builder{}); err != errUsage {
		t.Fatalf("err = %v, want usage", err)
	}
}
