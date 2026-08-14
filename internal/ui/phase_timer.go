package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// phaseTimer records how long each reported phase of a long operation took.
//
// Session creation reports progress through a status callback as it moves across
// SSH, git, tmux and mutagen. Timing those same messages turns "it took ages"
// into a named phase and a duration, without adding instrumentation points that
// can drift from the phases the user actually sees.
type phaseTimer struct {
	now     func() time.Time
	started time.Time
	last    time.Time
	current string
	order   []string
	totals  map[string]time.Duration
}

func newPhaseTimer() *phaseTimer {
	return newPhaseTimerWithClock(time.Now)
}

func newPhaseTimerWithClock(now func() time.Time) *phaseTimer {
	t := now()
	return &phaseTimer{
		now:     now,
		started: t,
		last:    t,
		totals:  make(map[string]time.Duration),
	}
}

// mark closes the phase in progress and opens the one named by status.
func (p *phaseTimer) mark(status string) {
	if p == nil {
		return
	}
	status = strings.TrimSpace(status)
	if status == "" {
		return
	}
	now := p.now()
	p.closeCurrent(now)
	p.current = status
	p.last = now
}

func (p *phaseTimer) closeCurrent(now time.Time) {
	if p.current == "" {
		return
	}
	if _, seen := p.totals[p.current]; !seen {
		p.order = append(p.order, p.current)
	}
	// Repeated statuses accumulate: mutagen in particular reports the same
	// "Sync: …" line many times while it settles.
	p.totals[p.current] += now.Sub(p.last)
}

// finish closes the final phase and reports the breakdown, slowest first, via
// the supplied logger.
func (p *phaseTimer) finish(logf func(string, ...interface{})) {
	if p == nil || logf == nil {
		return
	}
	now := p.now()
	p.closeCurrent(now)
	p.current = ""

	total := now.Sub(p.started)
	logf("session create took %s across %d phases:", total.Round(time.Millisecond), len(p.order))
	for _, name := range p.slowestFirst() {
		logf("  %8s  %s", p.totals[name].Round(time.Millisecond), name)
	}
}

// slowestFirst orders phases by duration, breaking ties by first occurrence so
// the output is stable.
func (p *phaseTimer) slowestFirst() []string {
	names := append([]string(nil), p.order...)
	index := make(map[string]int, len(p.order))
	for i, n := range p.order {
		index[n] = i
	}
	sort.SliceStable(names, func(i, j int) bool {
		a, b := p.totals[names[i]], p.totals[names[j]]
		if a != b {
			return a > b
		}
		return index[names[i]] < index[names[j]]
	})
	return names
}

// summary renders the same breakdown as a single line, for callers that want it
// without a logger.
func (p *phaseTimer) summary() string {
	if p == nil {
		return ""
	}
	parts := make([]string, 0, len(p.order))
	for _, name := range p.slowestFirst() {
		parts = append(parts, fmt.Sprintf("%s=%s", name, p.totals[name].Round(time.Millisecond)))
	}
	return strings.Join(parts, " ")
}
