package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/limits"
	"github.com/bouwerp/aiman/internal/usecase"
)

func usageLimitsModel(t *testing.T) (*Model, string) {
	t.Helper()
	m := NewModel(twoRemoteCfg(), nil, nil, nil, nil, nil, nil)
	m.state = viewStateMain
	m.currentTab = tabSessions
	updated, _ := m.Update(pressRune('$'))
	m = updated.(*Model)
	return m, m.usageView.hosts[0].host
}

func oneReading() limits.Snapshot {
	return limits.Snapshot{Readings: []limits.Reading{{
		Provider: "muse", Plan: "High", State: limits.StateOK, ObservedAt: time.Now(),
		Windows: []limits.Window{{Label: "week", UsedPercent: 37}},
	}}}
}

func TestUsageLimitsShownUnderTheirHost(t *testing.T) {
	m, host := usageLimitsModel(t)
	m.applyUsageLimits(usageLimitsMsg{host: host, snap: oneReading()})
	if view := m.usageView.viewString(); !strings.Contains(view, "37%") || !strings.Contains(view, "muse") {
		t.Fatalf("view has no limits row:\n%s", view)
	}
}

func TestUsageLimitsPollWhileRefreshingAndStop(t *testing.T) {
	m, host := usageLimitsModel(t)
	snap := oneReading()
	snap.Refreshing = true
	if cmd := m.applyUsageLimits(usageLimitsMsg{host: host, snap: snap}); cmd == nil {
		t.Fatal("a refreshing host must be polled")
	}
	snap.Refreshing = false
	if cmd := m.applyUsageLimits(usageLimitsMsg{host: host, snap: snap}); cmd != nil {
		t.Fatal("a finished host must not be polled")
	}
}

func TestUsageLimitsPollStopsWhenTheViewCloses(t *testing.T) {
	m, host := usageLimitsModel(t)
	updated, _ := m.Update(pressKey("esc"))
	m = updated.(*Model)
	snap := oneReading()
	snap.Refreshing = true
	if cmd := m.applyUsageLimits(usageLimitsMsg{host: host, snap: snap}); cmd != nil {
		t.Fatal("a closed view must not keep polling")
	}
}

func TestUsageLimitsOldServeShowsNothing(t *testing.T) {
	m, host := usageLimitsModel(t)
	before := m.usageView.viewString()
	err := fmt.Errorf("limits.get: %w", usecase.ErrRPCUnsupported)
	if cmd := m.applyUsageLimits(usageLimitsMsg{host: host, err: err}); cmd != nil {
		t.Fatal("an old serve must not be polled")
	}
	if after := m.usageView.viewString(); after != before {
		t.Fatalf("an old serve changed the view:\n%s", after)
	}
}

func TestUsageLimitsErrorKeepsCostAndLastRows(t *testing.T) {
	m, host := usageLimitsModel(t)
	m.applyUsageLimits(usageLimitsMsg{host: host, snap: oneReading()})
	if cmd := m.applyUsageLimits(usageLimitsMsg{host: host, err: errors.New("ssh: gone")}); cmd != nil {
		t.Fatal("a failed call must not be polled")
	}
	if view := m.usageView.viewString(); !strings.Contains(view, "37%") {
		t.Fatalf("a failed call dropped the last rows:\n%s", view)
	}
}

func TestUsageLimitsKeyForcesARefresh(t *testing.T) {
	m, _ := usageLimitsModel(t)
	updated, cmd := m.Update(pressRune('l'))
	m = updated.(*Model)
	if m.state != viewStateUsage {
		t.Fatalf("state = %v", m.state)
	}
	if cmd == nil {
		t.Fatal("l must fetch limits")
	}
	if batch, ok := cmd().(tea.BatchMsg); !ok || len(batch) != len(m.usageView.hosts) {
		t.Fatalf("l must ask every host, got %T", cmd())
	}
}

func TestUsageLimitsResultReachesTheViewFromAnyScreen(t *testing.T) {
	m, host := usageLimitsModel(t)
	updated, _ := m.Update(pressKey("esc"))
	m = updated.(*Model)
	updated, _ = m.Update(usageLimitsMsg{host: host, snap: oneReading()})
	m = updated.(*Model)
	m.state = viewStateUsage
	if view := m.usageView.viewString(); !strings.Contains(view, "37%") {
		t.Fatalf("a result that landed off the screen was dropped:\n%s", view)
	}
}
