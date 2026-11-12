package cgroup

import (
	"testing"
	"time"
)

func sessSnap(cur int64, pressure float64) Snapshot {
	return Snapshot{Supported: true, Sessions: []SessionUsage{{ID: "s1", Name: "fix", Usage: Usage{
		MemoryCurrent: cur, MemoryHigh: 100, MemoryPressure: pressure,
	}}}}
}

func TestAlerterHoldsThirtySecondsNearTheLimit(t *testing.T) {
	a := NewAlerter()
	t0 := time.Unix(0, 0)
	for i, sec := range []int{0, 10, 20} {
		if got := a.Evaluate(sessSnap(95, 0), t0.Add(time.Duration(sec)*time.Second)); len(got) != 0 {
			t.Fatalf("sample %d alerted before the hold: %+v", i, got)
		}
	}
	got := a.Evaluate(sessSnap(95, 0), t0.Add(30*time.Second))
	if len(got) != 1 || got[0].Scope != AlertSession || got[0].SessionID != "s1" || got[0].Throttled {
		t.Fatalf("after the hold: %+v", got)
	}
	if got[0].Text() != "95/100 memory" {
		t.Fatalf("Text = %q", got[0].Text())
	}
}

func TestAlerterPressureAlertsAtOnce(t *testing.T) {
	a := NewAlerter()
	got := a.Evaluate(sessSnap(40, 61), time.Unix(0, 0))
	if len(got) != 1 || !got[0].Throttled || got[0].Text() != "40/100 memory, pressure 61%" {
		t.Fatalf("pressure alert: %+v", got)
	}
}

func TestAlerterRateLimitAndHysteresis(t *testing.T) {
	a := NewAlerter()
	t0 := time.Unix(0, 0)
	if len(a.Evaluate(sessSnap(95, 60), t0)) != 1 {
		t.Fatal("no first alert")
	}
	// Still over: silent for ten minutes, then once more.
	if got := a.Evaluate(sessSnap(99, 70), t0.Add(9*time.Minute)); len(got) != 0 {
		t.Fatalf("alerted again inside ten minutes: %+v", got)
	}
	if got := a.Evaluate(sessSnap(99, 70), t0.Add(10*time.Minute)); len(got) != 1 {
		t.Fatalf("no repeat after ten minutes: %+v", got)
	}
	// A dip that stays above the clear thresholds does not restart the hold.
	if got := a.Evaluate(sessSnap(85, 0), t0.Add(11*time.Minute)); len(got) != 0 {
		t.Fatalf("dip alerted: %+v", got)
	}
	if a.states["s1"].over != true {
		t.Fatal("a dip to 85% cleared the alert")
	}
	// A real recovery clears it; the next crossing waits out the hold and
	// the ten minutes since the last alert.
	a.Evaluate(sessSnap(50, 0), t0.Add(12*time.Minute))
	if a.states["s1"].over {
		t.Fatal("recovery did not clear")
	}
	if got := a.Evaluate(sessSnap(95, 0), t0.Add(13*time.Minute)); len(got) != 0 {
		t.Fatalf("alerted without the hold: %+v", got)
	}
	if got := a.Evaluate(sessSnap(95, 0), t0.Add(21*time.Minute)); len(got) != 1 {
		t.Fatalf("no alert after recovery and a new crossing: %+v", got)
	}
}

func TestAlerterSlicesAndForgetting(t *testing.T) {
	a := NewAlerter()
	t0 := time.Unix(0, 0)
	snap := Snapshot{
		Supported:     true,
		UserSlice:     &Usage{MemoryCurrent: 91, MemoryHigh: 100, MemoryPressure: 80},
		SessionsSlice: &Usage{MemoryCurrent: 10, MemoryHigh: Infinity, MemoryPressure: 80},
		Sessions:      []SessionUsage{{ID: "gone", Usage: Usage{MemoryCurrent: 99, MemoryHigh: 100}}},
	}
	got := a.Evaluate(snap, t0)
	if len(got) != 1 || got[0].Scope != AlertUserSlice {
		t.Fatalf("slice alerts: %+v", got)
	}
	snap.Sessions = nil
	a.Evaluate(snap, t0.Add(time.Second))
	if _, kept := a.states["gone"]; kept {
		t.Fatal("a session that left the snapshot kept its state")
	}
	if got := a.Evaluate(Snapshot{}, t0); got != nil {
		t.Fatalf("unsupported snapshot alerted: %+v", got)
	}
}
