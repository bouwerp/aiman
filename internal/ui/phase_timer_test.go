package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

type fakeClock struct {
	t time.Time
}

func (c *fakeClock) now() time.Time { return c.t }
func (c *fakeClock) advance(d time.Duration) {
	c.t = c.t.Add(d)
}

func TestPhaseTimerAttributesTimeToTheReportedPhase(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	p := newPhaseTimerWithClock(clock.now)

	p.mark("Creating session...")
	clock.advance(2 * time.Second)
	p.mark("Preparing file sync...")
	clock.advance(500 * time.Millisecond)
	p.mark("Starting file sync...")
	clock.advance(30 * time.Second)

	var lines []string
	p.finish(func(format string, args ...interface{}) {
		lines = append(lines, fmt.Sprintf(format, args...))
	})

	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "session create took 32.5s") {
		t.Errorf("expected the total, got:\n%s", joined)
	}
	// Slowest first, so the phase to investigate is the first one read.
	if len(lines) < 2 || !strings.Contains(lines[1], "Starting file sync...") {
		t.Errorf("expected the slowest phase first, got:\n%s", joined)
	}
	if !strings.Contains(joined, "30s") || !strings.Contains(joined, "2s") {
		t.Errorf("expected per-phase durations, got:\n%s", joined)
	}
}

// mutagen reports the same "Sync: …" status repeatedly while it settles; those
// need to accumulate into one entry rather than each resetting the clock.
func TestPhaseTimerAccumulatesRepeatedStatuses(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	p := newPhaseTimerWithClock(clock.now)

	p.mark("Sync: Scanning")
	clock.advance(3 * time.Second)
	p.mark("Sync: Scanning")
	clock.advance(4 * time.Second)
	p.mark("done")
	clock.advance(time.Second)
	p.finish(func(string, ...interface{}) {})

	if got := p.totals["Sync: Scanning"]; got != 7*time.Second {
		t.Errorf("repeated status total = %s, want 7s", got)
	}
	if n := len(p.order); n != 2 {
		t.Errorf("expected 2 distinct phases, got %d: %v", n, p.order)
	}
}

func TestPhaseTimerIgnoresBlankStatuses(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	p := newPhaseTimerWithClock(clock.now)
	p.mark("real")
	clock.advance(time.Second)
	p.mark("")
	p.mark("   ")
	clock.advance(time.Second)
	p.finish(func(string, ...interface{}) {})

	if len(p.order) != 1 || p.order[0] != "real" {
		t.Errorf("blank statuses should not create phases, got %v", p.order)
	}
	if got := p.totals["real"]; got != 2*time.Second {
		t.Errorf("real phase = %s, want 2s", got)
	}
}

func TestPhaseTimerSummaryIsSlowestFirst(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	p := newPhaseTimerWithClock(clock.now)
	p.mark("fast")
	clock.advance(time.Second)
	p.mark("slow")
	clock.advance(10 * time.Second)
	p.finish(func(string, ...interface{}) {})

	got := p.summary()
	if !strings.HasPrefix(got, "slow=10s") {
		t.Errorf("summary = %q, want it to lead with the slowest phase", got)
	}
}

// A nil timer must be safe: createSession installs one unconditionally.
func TestPhaseTimerNilSafe(t *testing.T) {
	var p *phaseTimer
	p.mark("x")
	p.finish(func(string, ...interface{}) {})
	if p.summary() != "" {
		t.Error("expected empty summary from a nil timer")
	}
}
