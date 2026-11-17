package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/cgroup"
	"github.com/bouwerp/aiman/internal/harness"
)

const gib = int64(1) << 30

// fakeHarnessMemory is the gate's harness memory source.
type fakeHarnessMemory struct {
	mu  sync.Mutex
	m   harness.Memory
	err error
}

func (f *fakeHarnessMemory) read(context.Context) (harness.Memory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.m, f.err
}

func (f *fakeHarnessMemory) set(runs ...harness.RunMemory) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m, f.err = harness.Memory{Runs: runs, Source: "cgroup"}, nil
	for _, r := range runs {
		f.m.HeldBytes += r.HeldBytes
	}
}

func (f *fakeHarnessMemory) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// sliceTick steps the gate with the user slice holding held of a 24G
// memory.high at full avg10 full.
func sliceTick(srv *Server, f *fakePressure, held int64, full float64) PressureGate {
	f.advance(5 * time.Second)
	f.mu.Lock()
	f.r = PressureReading{Memory: cgroup.Pressure{SomeAvg10: full, FullAvg10: full}, SliceHeld: held, SliceHigh: 24 * gib}
	f.mu.Unlock()
	srv.stepPressure(context.Background())
	return srv.Pressure()
}

func TestPressureCountsHarnessRSS(t *testing.T) {
	srv, _, _ := todoServer(t)
	f := withFakePressure(srv)

	// Without the harness, a slice holding 20G of 24G at full 0 is open.
	if g := sliceTick(srv, f, 20*gib, 0); g.Closed || g.Harness != nil {
		t.Fatalf("gate %+v, want open with no harness report", g)
	}

	hm := &fakeHarnessMemory{}
	srv.SetHarnessMemory(hm.read)
	hm.set(harness.RunMemory{Run: "r1", Project: "harness-r1", HeldBytes: 2 * gib, Containers: 3})
	g := sliceTick(srv, f, 20*gib, 0)
	if g.Closed {
		t.Fatalf("closed at 22G of 24G: %+v", g)
	}
	if h := g.Harness; h == nil || h.HeldBytes != 2*gib || h.SliceHeldBytes != 20*gib || h.BudgetBytes != 24*gib || h.OverBudget || len(h.Runs) != 1 || h.Runs[0].Run != "r1" {
		t.Fatalf("harness report %+v, want 2G held, 20G slice, 24G budget, run r1", g.Harness)
	}

	// A second run brings the sum to the slice's memory.high: the gate closes
	// with full avg10 still 0.
	hm.set(harness.RunMemory{Run: "r1", Project: "harness-r1", HeldBytes: 2 * gib, Containers: 3},
		harness.RunMemory{Run: "r2", Project: "harness-r2", HeldBytes: 2 * gib, Containers: 2})
	g = sliceTick(srv, f, 20*gib, 0)
	if !g.Closed || g.Reason != GateReasonHarnessBudget || !g.Harness.OverBudget || g.Harness.HeldBytes != 4*gib {
		t.Fatalf("gate %+v (harness %+v), want closed for the harness budget at 24G", g, g.Harness)
	}
	closedAt := f.clock()

	resp := rpc(t, srv, "", "harness.up", map[string]any{"scenario": "login"})
	if resp.Error == nil || resp.Error.Code != CodeHostUnderPressure {
		t.Fatalf("harness.up: %+v, want %s", resp.Error, CodeHostUnderPressure)
	}
	raw, _ := json.Marshal(resp.Error.Data)
	var data PressureRefusalData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if data.Reason != GateReasonHarnessBudget || data.HarnessHeldBytes != 4*gib || data.RetryAfterMS != 30000 || !data.Since.Equal(closedAt) {
		t.Fatalf("refusal data %s", raw)
	}

	// host.stats and resources.get report the same gate.
	for _, method := range []string{"host.stats", "resources.get"} {
		r := rpc(t, srv, "", method, nil)
		raw, _ := json.Marshal(r.Result)
		var out struct {
			Gate *PressureGate `json:"gate"`
		}
		if r.Result == nil || json.Unmarshal(raw, &out) != nil {
			t.Fatalf("%s: %+v", method, r.Error)
		}
		if method == "resources.get" {
			var res struct {
				Resources struct {
					Gate *PressureGate `json:"gate"`
				} `json:"resources"`
			}
			_ = json.Unmarshal(raw, &res)
			out.Gate = res.Resources.Gate
		}
		if out.Gate == nil || out.Gate.Harness == nil || out.Gate.Reason != GateReasonHarnessBudget || len(out.Gate.Harness.Runs) != 2 {
			t.Fatalf("%s gate %s, want the harness budget closing with two runs", method, raw)
		}
	}

	// 23G is under memory.high but not under 90% of it: the gate stays closed.
	hm.set(harness.RunMemory{Run: "r1", Project: "harness-r1", HeldBytes: 3 * gib, Containers: 3})
	for range 8 {
		if g := sliceTick(srv, f, 20*gib, 0); !g.Closed {
			t.Fatalf("opened at 23G of 24G: %+v", g)
		}
	}

	// A failed read keeps the last good one and reports the error.
	hm.fail(errors.New("docker ps: exit 1"))
	g = sliceTick(srv, f, 20*gib, 0)
	if !g.Closed || g.Harness.HeldBytes != 3*gib || g.Harness.Error == "" {
		t.Fatalf("gate %+v (harness %+v), want closed on the last good read with the error", g, g.Harness)
	}

	// r1 stops: 20G is under 90% of 24G, and after the 30s hold the gate opens.
	hm.set()
	for range 6 {
		if g := sliceTick(srv, f, 20*gib, 0); !g.Closed {
			t.Fatalf("opened inside the hold: %+v", g)
		}
	}
	g = sliceTick(srv, f, 20*gib, 0)
	if g.Closed || g.Reason != "" || g.Harness == nil || g.Harness.HeldBytes != 0 {
		t.Fatalf("gate %+v, want open with no reason after the runs stopped", g)
	}

	// Memory full still closes it, with its own reason, harness or not.
	if g := sliceTick(srv, f, 4*gib, 35); !g.Closed || g.Reason != GateReasonMemoryFull {
		t.Fatalf("gate %+v, want closed for memory full", g)
	}

	// Full falls back, but a run now fills the budget: the gate stays
	// closed and names the harness budget.
	hm.set(harness.RunMemory{Run: "r3", Project: "harness-r3", HeldBytes: 6 * gib, Containers: 2})
	if g := sliceTick(srv, f, 20*gib, 2); !g.Closed || g.Reason != GateReasonHarnessBudget {
		t.Fatalf("gate %+v, want closed for the harness budget", g)
	}
}

func TestPressureHarnessWithoutBudgetOnlyReports(t *testing.T) {
	srv, _, _ := todoServer(t)
	f := withFakePressure(srv)
	hm := &fakeHarnessMemory{}
	srv.SetHarnessMemory(hm.read)
	hm.set(harness.RunMemory{Run: "r1", Project: "harness-r1", HeldBytes: 64 * gib, Containers: 1})
	f.advance(5 * time.Second)
	f.set(0, 0, false) // no SliceHigh: the slice has no finite memory.high
	srv.stepPressure(context.Background())
	g := srv.Pressure()
	if g.Closed || g.Harness == nil || g.Harness.HeldBytes != 64*gib || g.Harness.BudgetBytes != 0 || g.Harness.OverBudget {
		t.Fatalf("gate %+v (harness %+v), want open, reporting 64G with no budget", g, g.Harness)
	}
}
