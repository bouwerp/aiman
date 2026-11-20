package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/cgroup"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// CodeHostUnderPressure refuses new heavy work (a session, a helper, a review
// run) while the pressure gate is closed. Error.Data is a PressureRefusalData.
// The client does not retry it.
const CodeHostUnderPressure = "host_under_pressure"

// ErrHostUnderPressure is what a helper spawn returns while the gate is closed.
var ErrHostUnderPressure = errors.New("host under pressure")

const (
	// pressureEvery is how often runPressure reads the user slice.
	pressureEvery = 5 * time.Second
	// pressureRetryAfter is the retry hint a refusal carries.
	pressureRetryAfter = 30 * time.Second
	// pressureNoticeEvery spaces the host_pressure notifications.
	pressureNoticeEvery = 10 * time.Minute
	// harnessReopenRatio is the share of the user slice's memory.high that
	// the slice and the harness runs together must fall below before a gate
	// closed for the harness budget may open.
	harnessReopenRatio = 0.9
)

// Why a closed gate closed (PressureGate.Reason).
const (
	// GateReasonMemoryFull: the user slice's memory full avg10 reached the
	// close value.
	GateReasonMemoryFull = "memory_full"
	// GateReasonHarnessBudget: the memory the user slice holds plus the
	// memory harness runs hold reached the slice's memory.high.
	GateReasonHarnessBudget = "harness_budget"
)

// PressureRefusalData is host_under_pressure's Error.Data: the gate when it
// answered.
type PressureRefusalData struct {
	RetryAfterMS    int64     `json:"retry_after_ms"`
	MemoryFullAvg10 float64   `json:"memory_full_avg10"`
	MemorySomeAvg10 float64   `json:"memory_some_avg10"`
	Since           time.Time `json:"since"`
	// Reason and HarnessHeldBytes say why the gate is closed; the harness
	// bytes are set only for GateReasonHarnessBudget.
	Reason           string `json:"reason,omitempty"`
	HarnessHeldBytes int64  `json:"harness_held_bytes,omitempty"`
}

// HarnessGate is what the gate knows of harness memory. Harness containers
// run under docker.service, outside the user slice, so the slice's pressure
// and memory.current never see them. The gate counts them against the slice's
// memory.high, the budget the operator gave aiman's work: OverBudget is true
// when SliceHeldBytes plus HeldBytes reaches BudgetBytes, and that closes the
// gate as memory full avg10 does. BudgetBytes is 0 when the slice has no
// finite memory.high (or serve runs in no user slice); the harness memory is
// then reported only.
type HarnessGate struct {
	HeldBytes      int64               `json:"held_bytes"`
	SliceHeldBytes int64               `json:"slice_held_bytes,omitempty"`
	BudgetBytes    int64               `json:"budget_bytes,omitempty"`
	OverBudget     bool                `json:"over_budget"`
	Runs           []harness.RunMemory `json:"runs,omitempty"`
	Source         string              `json:"source,omitempty"`
	// Error is the last failed read of harness memory; the values above are
	// then the last good read.
	Error string `json:"error,omitempty"`
}

// PressureReading is one read of the user slice: its memory pressure, and
// whether memory.current is at or above memory.high (reported, never gates).
// SliceHeld is the slice's held memory (cgroup.Usage.Held) and SliceHigh its
// memory.high, 0 when it has none. Harness is the memory harness runs hold,
// nil when serve runs no harness; stepPressure fills it from the source
// SetHarnessMemory gives when the reader leaves it nil.
type PressureReading struct {
	Memory        cgroup.Pressure
	SliceOverHigh bool
	SliceHeld     int64
	SliceHigh     int64
	Harness       *harness.Memory
}

// pressureGate is the gate's state machine. It closes when full avg10 reaches
// the close value and opens once full avg10 has stayed below the reopen value
// for the hold. Nothing else moves it.
type pressureGate struct {
	mu   sync.Mutex
	read func() (PressureReading, error)
	now  func() time.Time
	cfg  config.PressureGateConfig
	gate PressureGate
	// read is true once a reading arrived.
	seen bool
	// below is when the current run of readings under the reopen value
	// started; zero while there is none.
	below time.Time
	// noticed is when the last host_pressure notification went out.
	noticed time.Time
	// lastErr is the last read error logged, so a failing reader logs once.
	lastErr string
	// onOpen runs, outside mu, each time the gate opens.
	onOpen []func()
	// harness reads the memory harness runs hold; nil without the harness.
	harness func(context.Context) (harness.Memory, error)
	// lastHarness is the last good harness read, kept across a failed one.
	lastHarness *harness.Memory
	// harnessErr is the last failed harness read, "" after a good one.
	harnessErr string
}

// pressure returns the server's gate, made on first use so a Server built
// without New still has one.
func (s *Server) pressure() *pressureGate {
	s.pressOnce.Do(func() {
		if s.press == nil {
			s.press = &pressureGate{read: readHostPressure, now: time.Now}
		}
	})
	return s.press
}

// SetPressureSource replaces the reader and the clock the gate uses.
func (s *Server) SetPressureSource(read func() (PressureReading, error), now func() time.Time) {
	g := s.pressure()
	g.mu.Lock()
	defer g.mu.Unlock()
	if read != nil {
		g.read = read
	}
	if now != nil {
		g.now = now
	}
}

// SetPressureGate applies host.load.pressure_gate. Disabling it opens a
// closed gate at its next step.
func (s *Server) SetPressureGate(c config.PressureGateConfig) {
	g := s.pressure()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cfg = c
}

// SetHarnessMemory gives the gate the memory harness runs hold, read on each
// step. nil stops counting it.
func (s *Server) SetHarnessMemory(read func(context.Context) (harness.Memory, error)) {
	g := s.pressure()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.harness = read
	g.lastHarness, g.harnessErr = nil, ""
}

// OnPressureOpen registers fn to run each time the gate opens, such as a
// queue that waits for it. fn runs on the gate's goroutine and must not block.
func (s *Server) OnPressureOpen(fn func()) {
	g := s.pressure()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.onOpen = append(g.onOpen, fn)
}

// Pressure is the gate's current state.
func (s *Server) Pressure() PressureGate {
	g := s.pressure()
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.gate
}

// PressureOpen reports whether new heavy work may start now: the question a
// queue asks before it starts a run.
func (s *Server) PressureOpen() bool { return !s.Pressure().Closed }

// LastPressure is the gate's last reading of memory some and full avg10, and
// false before the first one.
func (s *Server) LastPressure() (some, full float64, ok bool) {
	g := s.pressure()
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.gate.MemorySomeAvg10, g.gate.MemoryFullAvg10, g.seen
}

// RunPressure steps the gate every 5 seconds until ctx ends.
func (s *Server) RunPressure(ctx context.Context) {
	t := time.NewTicker(pressureEvery)
	defer t.Stop()
	s.stepPressure(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.stepPressure(ctx)
		}
	}
}

// stepPressure takes one reading and moves the gate. A failed read leaves the
// gate as it is.
func (s *Server) stepPressure(ctx context.Context) {
	g := s.pressure()
	g.mu.Lock()
	read, readHarness := g.read, g.harness
	g.mu.Unlock()
	r, err := read()
	var hm harness.Memory
	var herr error
	countHarness := err == nil && r.Harness == nil && readHarness != nil
	if countHarness {
		hm, herr = readHarness(ctx)
	}

	g.mu.Lock()
	if countHarness {
		g.useHarness(&r, hm, herr)
	}
	if err != nil {
		if msg := err.Error(); msg != g.lastErr {
			g.lastErr = msg
			log.Printf("pressure gate: read: %v", err)
		}
		g.mu.Unlock()
		return
	}
	g.lastErr = ""
	closed, opened, notice := g.step(r)
	state := g.gate
	hooks := g.onOpen
	g.mu.Unlock()

	switch {
	case closed && state.Reason == GateReasonHarnessBudget && state.Harness != nil:
		h := state.Harness
		log.Printf("pressure gate: closed: user slice %s plus harness runs %s reach memory.high %s",
			cgroup.FormatSize(h.SliceHeldBytes), cgroup.FormatSize(h.HeldBytes), cgroup.FormatSize(h.BudgetBytes))
		if notice {
			s.notifyHostPressure(ctx, state)
		}
	case closed:
		log.Printf("pressure gate: closed at memory full %.2f%% some %.2f%%", state.MemoryFullAvg10, state.MemorySomeAvg10)
		if notice {
			s.notifyHostPressure(ctx, state)
		}
	case opened:
		log.Printf("pressure gate: open at memory full %.2f%% some %.2f%%", state.MemoryFullAvg10, state.MemorySomeAvg10)
		for _, fn := range hooks {
			fn()
		}
	}
}

// useHarness puts a harness read into r under mu. A failed read logs once per
// distinct error and keeps the last good read.
func (g *pressureGate) useHarness(r *PressureReading, m harness.Memory, err error) {
	if err != nil {
		if msg := err.Error(); msg != g.harnessErr {
			log.Printf("pressure gate: harness memory: %v", err)
			g.harnessErr = msg
		}
	} else {
		g.lastHarness, g.harnessErr = &m, ""
	}
	if g.lastHarness != nil {
		r.Harness = g.lastHarness
	} else if g.harnessErr != "" {
		r.Harness = &harness.Memory{}
	}
}

// harnessBudget reports whether the user slice and the harness runs together
// hold at least the slice's memory.high (over), and whether they hold less
// than harnessReopenRatio of it (clear). With no harness memory or no finite
// memory.high there is no budget: never over, always clear.
func harnessBudget(r PressureReading) (over, clear bool) {
	if r.Harness == nil || r.Harness.HeldBytes <= 0 || r.SliceHigh <= 0 || r.SliceHigh == cgroup.Infinity {
		return false, true
	}
	total := r.SliceHeld + r.Harness.HeldBytes
	return total >= r.SliceHigh, float64(total) < harnessReopenRatio*float64(r.SliceHigh)
}

// harnessReport is the gate's harness report for r, nil without the harness.
func (g *pressureGate) harnessReport(r PressureReading, over bool) *HarnessGate {
	if r.Harness == nil {
		return nil
	}
	h := &HarnessGate{
		HeldBytes:  r.Harness.HeldBytes,
		OverBudget: over,
		Runs:       r.Harness.Runs,
		Source:     r.Harness.Source,
		Error:      g.harnessErr,
	}
	if r.SliceHigh > 0 && r.SliceHigh != cgroup.Infinity {
		h.SliceHeldBytes, h.BudgetBytes = r.SliceHeld, r.SliceHigh
	}
	return h
}

// step applies one reading under mu and reports whether the gate closed or
// opened, and whether a closing is due a notification.
func (g *pressureGate) step(r PressureReading) (closed, opened, notice bool) {
	now := g.now()
	g.seen = true
	g.gate.MemorySomeAvg10 = r.Memory.SomeAvg10
	g.gate.MemoryFullAvg10 = r.Memory.FullAvg10
	g.gate.SliceOverHigh = r.SliceOverHigh
	over, clear := harnessBudget(r)
	g.gate.Harness = g.harnessReport(r, over)
	full := r.Memory.FullAvg10
	if !g.cfg.On() {
		g.below = time.Time{}
		if g.gate.Closed {
			g.gate.Closed, g.gate.Since, g.gate.Reason = false, now, ""
			return false, true, false
		}
		return false, false, false
	}
	if !g.gate.Closed {
		memFull := full >= g.cfg.CloseOrDefault()
		if !memFull && !over {
			return false, false, false
		}
		g.gate.Reason = GateReasonMemoryFull
		if !memFull {
			g.gate.Reason = GateReasonHarnessBudget
		}
		g.gate.Closed, g.gate.Since, g.below = true, now, time.Time{}
		notice = g.noticed.IsZero() || now.Sub(g.noticed) >= pressureNoticeEvery
		if notice {
			g.noticed = now
		}
		return true, false, notice
	}
	if full >= g.cfg.ReopenOrDefault() || !clear {
		g.below = time.Time{}
		if full < g.cfg.ReopenOrDefault() {
			// Only the harness budget holds the gate now.
			g.gate.Reason = GateReasonHarnessBudget
		}
		return false, false, false
	}
	if g.below.IsZero() {
		g.below = now
	}
	if now.Sub(g.below) < g.cfg.HoldOrDefault() {
		return false, false, false
	}
	g.gate.Closed, g.gate.Since, g.below, g.gate.Reason = false, now, time.Time{}, ""
	return false, true, false
}

// notifyHostPressure emits host_pressure for a closing.
func (s *Server) notifyHostPressure(ctx context.Context, g PressureGate) {
	rec := s.NotifyRecorder()
	if rec == nil {
		return
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "host"
	}
	text := fmt.Sprintf("%s is under memory pressure: full %.0f%%; new sessions, reviews and CI runs wait", host, g.MemoryFullAvg10)
	if g.Reason == GateReasonHarnessBudget && g.Harness != nil {
		text = fmt.Sprintf("%s is at its memory budget: sessions %s plus harness runs %s of %s; new sessions, reviews, CI and harness runs wait",
			host, cgroup.FormatSize(g.Harness.SliceHeldBytes), cgroup.FormatSize(g.Harness.HeldBytes), cgroup.FormatSize(g.Harness.BudgetBytes))
	}
	if err := rec.Emit(ctx, "", "", domain.NotifyHostPressure, text); err != nil {
		log.Printf("pressure gate: notify: %v", err)
	}
}

// pressureGated are the methods that start heavy work and wait while the gate
// is closed. session.restart, session.terminate and pty.create are not here:
// restarting or ending a session is how memory comes back.
var pressureGated = map[string]bool{
	"session.create":  true,
	"review.run":      true,
	"review.coverage": true,
	"review.fix":      true,
	"review.assess":   true,
	"harness.up":      true,
	"overview.run":    true,
}

// pressureRefusal answers a gated method while the gate is closed, and nil
// when it may go on. force passes a closed gate for the operator; from a
// session it is needs_operator.
func (s *Server) pressureRefusal(ctx context.Context, req Request) *Response {
	if !pressureGated[req.Method] {
		return nil
	}
	var p struct {
		Force bool `json:"force"`
	}
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &p)
	}
	if p.Force {
		if s.isSessionCall(ctx, req) {
			r := errResp(req.ID, CodeNeedsOperator, "only the operator may force past the pressure gate")
			return &r
		}
		return nil
	}
	g := s.Pressure()
	if !g.Closed {
		return nil
	}
	msg := fmt.Sprintf("host under memory pressure (full avg10 %.1f%%) since %s: wait at least 30s, do not retry in a loop",
		g.MemoryFullAvg10, g.Since.Format(time.RFC3339))
	data := PressureRefusalData{
		RetryAfterMS:    pressureRetryAfter.Milliseconds(),
		MemoryFullAvg10: g.MemoryFullAvg10,
		MemorySomeAvg10: g.MemorySomeAvg10,
		Since:           g.Since,
		Reason:          g.Reason,
	}
	if g.Reason == GateReasonHarnessBudget && g.Harness != nil {
		data.HarnessHeldBytes = g.Harness.HeldBytes
		msg = fmt.Sprintf("host at its memory budget (sessions %s plus harness runs %s of %s) since %s: stop a harness run or wait at least 30s, do not retry in a loop",
			cgroup.FormatSize(g.Harness.SliceHeldBytes), cgroup.FormatSize(g.Harness.HeldBytes), cgroup.FormatSize(g.Harness.BudgetBytes), g.Since.Format(time.RFC3339))
	}
	r := errResp(req.ID, CodeHostUnderPressure, msg)
	r.Error.Data = data
	return &r
}

// gateReport is the gate for host.stats and resources.get.
func (s *Server) gateReport() *PressureGate {
	g := s.Pressure()
	return &g
}

// userSliceDir is the user-<uid>.slice serve runs in, found once; "" when it
// runs in none.
var userSliceDir = sync.OnceValue(func() string {
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	return cgroup.UserSliceOf(string(raw))
})

// errPressureUnsupported keeps the gate open where there is no PSI.
var errPressureUnsupported = errors.New("memory pressure is not available on this host")

// readHostPressure reads the user slice serve runs in, else the host's
// /proc/pressure/memory. Elsewhere than Linux it fails, which keeps the gate
// open.
func readHostPressure() (PressureReading, error) {
	if runtime.GOOS != "linux" {
		return PressureReading{}, errPressureUnsupported
	}
	if dir := userSliceDir(); dir != "" {
		if p, err := cgroup.ReadPressure(dir); err == nil {
			r := PressureReading{Memory: p, SliceOverHigh: cgroup.SliceOverHigh(dir)}
			r.SliceHeld, _ = cgroup.ReadHeld(dir)
			if high, ok := cgroup.ReadHigh(dir); ok && high != cgroup.Infinity {
				r.SliceHigh = high
			}
			return r, nil
		}
	}
	p, err := cgroup.ReadPressureFile(cgroup.HostPressureFile)
	if err != nil {
		return PressureReading{}, err
	}
	return PressureReading{Memory: p}, nil
}
