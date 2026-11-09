//go:build acceptance

package server

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/ptyruntime"
)

// Serve load guard P4 (docs/specs/serve-load-guard.md, Behaviour 5): the sweep
// period follows the 1-minute load average, and one pass captures at most 16
// sessions, transitions first, then stale or missing hook reports, then fresh
// working reports, round robin inside each group.
//
// Contract pinned here beyond the design's Contracts block:
//   - var sweepPeriodLow = 2s, sweepPeriodHigh = 5s; const sweepCaptureBudget = 16.
//   - sweepPeriod(load float64, ok bool, ncpu int) time.Duration is high when
//     ok and load >= ncpu, else low; runStateSweep re-reads it every tick.
//   - var readLoadAvg func() (float64, bool) reads /proc/loadavg on Linux and
//     answers false elsewhere; parseLoadAvg(string) (float64, bool) parses it.
//   - A session needs a capture when its hook report went to ended or
//     waiting_input since the last pass (a transition), when the report is
//     stale or missing, or when it is a fresh working report. A fresh idle,
//     ended or waiting_input report that did not just transition needs none,
//     is sampled without a capture and does not count toward the budget.
//   - Sessions past the budget keep their cached state until a later pass.

func TestSweepPeriodFollowsLoad(t *testing.T) {
	if sweepPeriodLow != 2*time.Second || sweepPeriodHigh != 5*time.Second {
		t.Fatalf("periods = %v/%v, want 2s/5s", sweepPeriodLow, sweepPeriodHigh)
	}
	if sweepCaptureBudget != 16 {
		t.Fatalf("sweepCaptureBudget = %d, want 16", sweepCaptureBudget)
	}
	cases := []struct {
		load float64
		ok   bool
		ncpu int
		want time.Duration
	}{
		{1, true, 4, sweepPeriodLow},
		{3.9, true, 4, sweepPeriodLow},
		{4, true, 4, sweepPeriodHigh},
		{120, true, 32, sweepPeriodHigh},
		{120, false, 4, sweepPeriodLow},
	}
	for _, c := range cases {
		if got := sweepPeriod(c.load, c.ok, c.ncpu); got != c.want {
			t.Errorf("sweepPeriod(%v, %v, %d) = %v, want %v", c.load, c.ok, c.ncpu, got, c.want)
		}
	}
}

func TestParseLoadAvg(t *testing.T) {
	v, ok := parseLoadAvg("0.52 0.58 0.59 1/1234 5678\n")
	if !ok || v != 0.52 {
		t.Fatalf("parseLoadAvg = %v, %v; want 0.52, true", v, ok)
	}
	for _, bad := range []string{"", "garbage", "x 1 2"} {
		if _, ok := parseLoadAvg(bad); ok {
			t.Errorf("parseLoadAvg(%q) ok, want false", bad)
		}
	}
	_, ok = readLoadAvg()
	if ok != (runtime.GOOS == "linux") {
		t.Errorf("readLoadAvg ok = %v on %s", ok, runtime.GOOS)
	}
}

func hookRow(id string, state domain.AgentState, at time.Time) domain.Session {
	s := sweepRow(id)
	s.HookState = state
	s.HookStateSource = agenthook.SourceLifecycle
	s.HookStateAt = at
	return s
}

func sweepIDs(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = prefix + "-" + string(rune('a'+i/26)) + string(rune('a'+i%26))
	}
	return out
}

func TestSweepCapturesAtMostSixteenAndRotates(t *testing.T) {
	names := sweepIDs("stale", 40)
	rows := make([]domain.Session, 0, len(names))
	for _, id := range names {
		rows = append(rows, sweepRow(id))
	}
	srv, pty, _ := ptyServer(t, rows...)
	for _, id := range names {
		pty.Add(ptyruntime.SessionInfo{ID: id, Name: id, Dir: "/w/" + id, Status: ptyruntime.StatusRunning})
	}
	ctx := context.Background()
	srv.sweepStates(ctx)
	if n := pty.Captures(); n != 16 {
		t.Fatalf("captures after one pass = %d, want 16", n)
	}
	srv.sweepStates(ctx)
	if n := pty.Captures(); n != 32 {
		t.Fatalf("captures after two passes = %d, want 32", n)
	}
	for _, id := range names {
		if c := pty.CapturesOf(id); c > 1 {
			t.Fatalf("%s captured %d times in two passes, want at most once (round robin)", id, c)
		}
	}
	srv.sweepStates(ctx)
	if n := pty.Captures(); n != 48 {
		t.Fatalf("captures after three passes = %d, want 48", n)
	}
	for _, id := range names {
		if pty.CapturesOf(id) == 0 {
			t.Errorf("%s never captured in ceil(40/16) = 3 passes", id)
		}
	}
}

func TestSweepOrdersTransitionsThenStaleThenWorking(t *testing.T) {
	stale := sweepIDs("stale", 16)
	working := []string{"work-1", "work-2"}
	waiting := []string{"wait-1", "wait-2"}
	var rows []domain.Session
	for _, id := range stale {
		rows = append(rows, sweepRow(id))
	}
	for _, id := range working {
		rows = append(rows, hookRow(id, domain.AgentStateWorking, time.Now()))
	}
	for _, id := range waiting {
		rows = append(rows, sweepRow(id))
	}
	srv, pty, _ := ptyServer(t, rows...)
	for _, r := range rows {
		pty.Add(ptyruntime.SessionInfo{ID: r.ID, Name: r.ID, Dir: "/w/" + r.ID, Status: ptyruntime.StatusRunning})
	}
	ctx := context.Background()

	// Pass 1: 18 stale sessions compete for 16 captures; working waits.
	srv.sweepStates(ctx)
	if n := pty.Captures(); n != 16 {
		t.Fatalf("pass 1 captures = %d, want 16", n)
	}
	for _, id := range working {
		if pty.CapturesOf(id) != 0 {
			t.Fatalf("%s (fresh working) captured before the stale group was served", id)
		}
	}

	// The two waiting sessions' hooks now report waiting_input: a transition.
	for _, id := range waiting {
		row := hookRow(id, domain.AgentStateWaitingInput, time.Now())
		if err := srv.repo.Save(ctx, &row); err != nil {
			t.Fatal(err)
		}
	}
	srv.sweepStates(ctx)
	if n := pty.Captures(); n != 32 {
		t.Fatalf("pass 2 captures = %d, want 32", n)
	}
	for _, id := range waiting {
		if pty.CapturesOf(id) != 1 {
			t.Errorf("%s transitioned and was captured %d times in pass 2, want 1", id, pty.CapturesOf(id))
		}
		if st, ok := srv.cachedSessionState(id); !ok || st.State != string(domain.AgentStateWaitingInput) {
			t.Errorf("%s cached state = %+v, want waiting_input", id, st)
		}
	}
	for _, id := range working {
		if pty.CapturesOf(id) != 0 {
			t.Errorf("%s (fresh working) captured while stale sessions were still waiting", id)
		}
	}

	// Pass 3: nothing transitioned; a fresh waiting_input report needs no capture.
	srv.sweepStates(ctx)
	for _, id := range waiting {
		if pty.CapturesOf(id) != 1 {
			t.Errorf("%s captured %d times after pass 3, want still 1", id, pty.CapturesOf(id))
		}
	}
	if n := pty.Captures(); n != 48 {
		t.Fatalf("pass 3 captures = %d, want 48", n)
	}
}

func TestSweepSamplesFreshIdleAndEndedWithoutCapture(t *testing.T) {
	now := time.Now()
	ended := sweepRow("ended-1")
	ended.AgentEnded = true
	rows := []domain.Session{
		hookRow("idle-1", domain.AgentStateIdle, now),
		hookRow("idle-2", domain.AgentStateIdle, now),
		ended,
		sweepRow("stale-1"),
		sweepRow("stale-2"),
	}
	srv, pty, _ := ptyServer(t, rows...)
	for _, r := range rows {
		pty.Add(ptyruntime.SessionInfo{ID: r.ID, Name: r.ID, Dir: "/w/" + r.ID, Status: ptyruntime.StatusRunning})
	}
	srv.sweepStates(context.Background())
	if n := pty.Captures(); n != 2 {
		t.Fatalf("captures = %d, want 2 (only the hook-less sessions)", n)
	}
	for _, id := range []string{"idle-1", "idle-2", "ended-1"} {
		st, ok := srv.cachedSessionState(id)
		if !ok || st.State != string(domain.AgentStateIdle) {
			t.Errorf("%s cached state = %+v, %v; want idle from the hook without a capture", id, st, ok)
		}
	}
}

func TestSweepTickerFollowsTheLoadAverage(t *testing.T) {
	run := func(load float64) int {
		old := readLoadAvg
		readLoadAvg = func() (float64, bool) { return load, true }
		defer func() { readLoadAvg = old }()
		srv, pty, _ := ptyServer(t, sweepRow("one"))
		pty.Add(ptyruntime.SessionInfo{ID: "one", Name: "one", Dir: "/w/one", Status: ptyruntime.StatusRunning})
		ctx, cancel := context.WithTimeout(context.Background(), 4500*time.Millisecond)
		defer cancel()
		srv.runStateSweep(ctx)
		return pty.Captures()
	}
	if n := run(float64(runtime.NumCPU()) * 4); n != 0 {
		t.Errorf("under load the sweep ran %d times in 4.5s, want 0 (period 5s)", n)
	}
	if n := run(0); n != 2 {
		t.Errorf("idle host: the sweep ran %d times in 4.5s, want 2 (period 2s)", n)
	}
}
