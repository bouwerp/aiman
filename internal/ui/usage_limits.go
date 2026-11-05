package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/limits"
)

// limitRows renders readings as one line each, with their age. Stub until P5.
func limitRows(_ []limits.Reading, _ time.Time) []string { return nil }

// usageLimitsMsg is one host's limits.get answer for the $ view.
type usageLimitsMsg struct {
	host string
	snap limits.Snapshot
	err  error
}

// applyUsageLimits stores a host's answer and returns the next poll, or nil. Stub until P5.
func (m *Model) applyUsageLimits(_ usageLimitsMsg) tea.Cmd { return nil }
