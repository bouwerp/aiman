package limits

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type fakeSource struct {
	name  string
	age   time.Duration
	runs  atomic.Int32
	block chan struct{}
	read  func() Reading
}

func (f *fakeSource) Name() string           { return f.name }
func (f *fakeSource) MinAge() time.Duration  { return f.age }
func (f *fakeSource) Timeout() time.Duration { return 5 * time.Second }
func (f *fakeSource) Read(context.Context) Reading {
	f.runs.Add(1)
	if f.block != nil {
		<-f.block
	}
	if f.read != nil {
		return f.read()
	}
	return Reading{Provider: f.name, State: StateOK, ObservedAt: testNow}
}

func settle(t *testing.T, m *Manager) Snapshot {
	t.Helper()
	for range 500 {
		if s := m.Snapshot(false); !s.Refreshing {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("manager never settled")
	return Snapshot{}
}

func TestManagerSnapshotDoesNotWaitForASource(t *testing.T) {
	f := &fakeSource{name: "slow", age: time.Hour, block: make(chan struct{})}
	defer close(f.block)
	m := NewManager([]Source{f}, func() time.Time { return testNow })
	done := make(chan Snapshot, 1)
	go func() { done <- m.Snapshot(false) }()
	select {
	case s := <-done:
		if !s.Refreshing {
			t.Fatal("first snapshot with a running source must say refreshing")
		}
	case <-time.After(time.Second):
		t.Fatal("Snapshot blocked on a running source")
	}
}

func TestManagerOneRunPerSourceAndForceJoins(t *testing.T) {
	f := &fakeSource{name: "grok", age: time.Hour, block: make(chan struct{})}
	m := NewManager([]Source{f}, func() time.Time { return testNow })
	m.Snapshot(false)
	for range 5 {
		m.Snapshot(true)
	}
	time.Sleep(50 * time.Millisecond)
	if got := f.runs.Load(); got != 1 {
		t.Fatalf("runs while in flight = %d, want 1", got)
	}
	close(f.block)
	settle(t, m)
}

func TestManagerSourcesDoNotWaitForEachOther(t *testing.T) {
	slow := &fakeSource{name: "slow", age: time.Hour, block: make(chan struct{})}
	defer close(slow.block)
	fast := &fakeSource{name: "fast", age: time.Hour}
	m := NewManager([]Source{slow, fast}, func() time.Time { return testNow })
	m.Snapshot(false)
	for range 200 {
		for _, r := range m.Snapshot(false).Readings {
			if r.Provider == "fast" && r.State == StateOK {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("fast source waited for the slow one")
}

func TestManagerRespectsMinAgeUnlessForced(t *testing.T) {
	now := testNow
	f := &fakeSource{name: "a", age: time.Minute}
	m := NewManager([]Source{f}, func() time.Time { return now })
	m.Snapshot(false)
	settle(t, m)
	m.Snapshot(false)
	settle(t, m)
	if got := f.runs.Load(); got != 1 {
		t.Fatalf("runs within min age = %d, want 1", got)
	}
	m.Snapshot(true)
	settle(t, m)
	if got := f.runs.Load(); got != 2 {
		t.Fatalf("forced runs = %d, want 2", got)
	}
	now = now.Add(2 * time.Minute)
	m.Snapshot(false)
	settle(t, m)
	if got := f.runs.Load(); got != 3 {
		t.Fatalf("runs past min age = %d, want 3", got)
	}
}

func TestManagerFailureKeepsOldNumbersWithTheirAge(t *testing.T) {
	old := testNow.Add(-time.Hour)
	good := Reading{Provider: "a", State: StateOK, ObservedAt: old, Windows: []Window{{Label: "week", UsedPercent: 19}}}
	bad := Reading{Provider: "a", State: StateUnknown, Reason: "screen changed"}
	cur := good
	f := &fakeSource{name: "a", age: time.Hour, read: func() Reading { return cur }}
	m := NewManager([]Source{f}, func() time.Time { return testNow })
	m.Snapshot(false)
	settle(t, m)
	cur = bad
	m.Snapshot(true)
	r := settle(t, m).Readings[0]
	if r.State != StateUnknown || r.Reason != "screen changed" {
		t.Fatalf("failure not reported: %+v", r)
	}
	if len(r.Windows) != 1 || r.Windows[0].UsedPercent != 19 || !r.ObservedAt.Equal(old) {
		t.Fatalf("old numbers or their age lost: %+v", r)
	}
}

func TestManagerReportsPassedWindowAsReset(t *testing.T) {
	f := &fakeSource{name: "a", age: time.Hour, read: func() Reading {
		return Reading{Provider: "a", State: StateOK, ObservedAt: testNow.Add(-6 * time.Hour), Windows: []Window{
			{Label: "5h", UsedPercent: 90, ResetsAt: testNow.Add(-time.Hour)},
			{Label: "week", UsedPercent: 30, ResetsAt: testNow.Add(time.Hour)},
		}}
	}}
	m := NewManager([]Source{f}, func() time.Time { return testNow })
	m.Snapshot(false)
	w := settle(t, m).Readings[0].Windows
	if !w[0].Reset || w[0].UsedPercent != 0 {
		t.Fatalf("a window past its reset must not show the old percent: %+v", w[0])
	}
	if w[1].Reset || w[1].UsedPercent != 30 {
		t.Fatalf("a live window must be untouched: %+v", w[1])
	}
}
