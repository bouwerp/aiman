package server

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// Overview step states.
const (
	StepPending = "pending"
	StepRunning = "running"
	StepDone    = "done"
	StepFailed  = "failed"
)

// Overview step names, in the order a run takes them.
const (
	StepCollect    = "collect"
	StepBundle     = "bundle"
	StepStartAgent = "start_agent"
	StepReport     = "report"
	StepCleanup    = "cleanup"
)

// overviewStepLabels are the steps in order with the text a client shows.
var overviewStepLabels = []struct{ name, label string }{
	{StepCollect, "collect sessions"},
	{StepBundle, "write bundle"},
	{StepStartAgent, "start agent"},
	{StepReport, "write report"},
	{StepCleanup, "clean up"},
}

// OverviewStep is one phase of a run. Name is a stable token; Label is what
// a client shows.
type OverviewStep struct {
	Name      string     `json:"name"`
	Label     string     `json:"label"`
	State     string     `json:"state"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Detail    string     `json:"detail,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// newOverviewSteps lists every step pending.
func newOverviewSteps() []OverviewStep {
	steps := make([]OverviewStep, 0, len(overviewStepLabels))
	for _, s := range overviewStepLabels {
		steps = append(steps, OverviewStep{Name: s.name, Label: s.label, State: StepPending})
	}
	return steps
}

// step is the run's step called name, or nil.
func (r *OverviewRun) step(name string) *OverviewStep {
	for i := range r.Steps {
		if r.Steps[i].Name == name {
			return &r.Steps[i]
		}
	}
	return nil
}

// clone copies the run so a reader never shares the live steps.
func (r *OverviewRun) clone() OverviewRun {
	c := *r
	c.Steps = append([]OverviewStep(nil), r.Steps...)
	return c
}

func (r *OverviewRun) ended() bool {
	return r.State == OverviewDone || r.State == OverviewFailed
}

// startStep marks a step running.
func (r *OverviewRun) startStep(name string, now time.Time) {
	if st := r.step(name); st != nil {
		st.State, st.StartedAt = StepRunning, &now
	}
}

// endStep marks a step done with its detail.
func (r *OverviewRun) endStep(name, detail string, now time.Time) {
	if st := r.step(name); st != nil {
		if st.StartedAt == nil {
			st.StartedAt = &now
		}
		st.State, st.EndedAt, st.Detail = StepDone, &now, detail
	}
}

// failRunningStep fails the running step with why and sets the run's error
// to "<label>: <why>", or to why when no step runs. Later steps stay pending.
func (r *OverviewRun) failRunningStep(why string, now time.Time) {
	r.Error = why
	for i := range r.Steps {
		st := &r.Steps[i]
		if st.State != StepRunning {
			continue
		}
		st.State, st.EndedAt, st.Error = StepFailed, &now, why
		r.Error = st.Label + ": " + why
		return
	}
}

// overviewBG is the context and wait group of serve's background overview
// work; CloseOverview cancels the one and waits for the other.
type overviewBG struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// overviewBGLocked returns the background state, made on first use. The
// caller holds overviewMu.
func (s *Server) overviewBGLocked() *overviewBG {
	if s.overview.bg == nil {
		ctx, cancel := context.WithCancel(context.Background())
		s.overview.bg = &overviewBG{ctx: ctx, cancel: cancel}
	}
	return s.overview.bg
}

// goOverview runs fn on serve's overview context, never a request's.
func (s *Server) goOverview(fn func(ctx context.Context)) {
	s.overviewMu.Lock()
	bg := s.overviewBGLocked()
	bg.wg.Add(1)
	s.overviewMu.Unlock()
	go func() {
		defer bg.wg.Done()
		fn(bg.ctx)
	}()
}

// CloseOverview cancels the background overview run and waits for it to
// record its end. Serve calls it before it closes the PTY runtime.
func (s *Server) CloseOverview() {
	s.overviewMu.Lock()
	bg := s.overviewBGLocked()
	s.overviewMu.Unlock()
	bg.cancel()
	bg.wg.Wait()
}

// OverviewStarting counts runs whose start_agent step has not ended: the
// host update gate waits for them.
func (s *Server) OverviewStarting() int {
	s.overviewMu.Lock()
	defer s.overviewMu.Unlock()
	r := s.overview.running
	if r == nil {
		return 0
	}
	if st := r.step(StepStartAgent); st != nil && (st.State == StepPending || st.State == StepRunning) {
		return 1
	}
	return 0
}

// updateOverview changes the run under the lock, then saves and publishes a
// copy, naming step on the hub event. It returns the copy.
func (s *Server) updateOverview(base string, run *OverviewRun, step string, fn func(r *OverviewRun, now time.Time)) OverviewRun {
	s.overviewMu.Lock()
	fn(run, time.Now().UTC())
	snap := run.clone()
	s.overviewMu.Unlock()
	s.saveOverview(base, &snap)
	s.publishOverview(&snap, step)
	return snap
}

// overviewPanic turns a panic in a run's goroutine into a failed run and
// frees the one-run lock.
func (s *Server) overviewPanic(base string, run *OverviewRun) {
	r := recover()
	if r == nil {
		return
	}
	log.Printf("overview %s: panic: %v", run.ID, r)
	why := fmt.Sprintf("panic: %v", r)
	s.overviewMu.Lock()
	var snap *OverviewRun
	if !run.ended() {
		now := time.Now().UTC()
		run.failRunningStep(why, now)
		run.State, run.EndedAt = OverviewFailed, &now
		c := run.clone()
		snap = &c
	}
	if s.overview.running == run {
		s.overview.running = nil
	}
	s.overviewMu.Unlock()
	if snap != nil {
		s.saveOverview(base, snap)
		s.publishOverview(snap, "")
	}
}
