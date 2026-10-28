package harness

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bouwerp/aiman/internal/handover"
	"github.com/bouwerp/aiman/internal/handoverchild"
	"github.com/bouwerp/aiman/internal/memoryjob"
	"github.com/bouwerp/aiman/internal/server"
)

// EnableHandovers attaches a memory-job queue and a real handoverchild.Manager
// to the harness server. Children are created by FakeCreator through the same
// session and terminal fakes the verifier uses, so a child never runs: the
// case plays the child by writing its outputs with WriteHandoverOutputs. A
// child counts as alive until cleanup stops it, so only the done marker or
// timeout ends a run.
func (h *Harness) EnableHandovers(timeout time.Duration) error {
	jobs, err := memoryjob.Open(h.Repo.DB())
	if err != nil {
		return err
	}
	h.srv.SetMemoryJobs(jobs)
	if h.terminal == nil {
		h.terminal = &evalTerminal{stopped: map[string]bool{}}
	}
	store := handoverchild.NewStore(filepath.Join(h.Dir, "handover-runs"))
	d := h.srv.HandoverDeps(store, func(string) bool { return true })
	d.Sessions = evalSessions{h}
	d.Terminal = h.terminal
	d.BaseDir = filepath.Join(h.Dir, "handovers")
	d.Poll = 10 * time.Millisecond
	d.Timeout = timeout
	m := handoverchild.NewManager(d)
	h.srv.SetHandovers(m)
	h.handovers = m
	h.handoverRuns = store
	return nil
}

// HandoverRun reads one handover child run.
func (h *Harness) HandoverRun(id string) (handoverchild.Run, error) {
	if h.handoverRuns == nil {
		return handoverchild.Run{}, errors.New("handovers are not enabled")
	}
	return h.handoverRuns.Get(id)
}

// HandoverActive reports whether the session has a starting or running child.
func (h *Harness) HandoverActive(sessionID string) bool {
	return h.handovers != nil && h.handovers.Active(sessionID)
}

// WaitHandoverRun polls until the run has finished and cleaned up.
func (h *Harness) WaitHandoverRun(id string) (handoverchild.Run, bool) {
	var got handoverchild.Run
	ok := waitUntil(10*time.Second, func() bool {
		r, err := h.HandoverRun(id)
		if err != nil {
			return false
		}
		got = r
		return !r.Status.Active() && r.CleanedUp
	})
	return got, ok
}

// WriteHandoverOutputs plays the child: once the run is running it writes
// handover.md and memories.json into the run's scratch directory, then the
// done marker last. An empty argument skips that file.
func (h *Harness) WriteHandoverOutputs(runID, md, memories string) error {
	var run handoverchild.Run
	if !waitUntil(5*time.Second, func() bool {
		r, err := h.HandoverRun(runID)
		run = r
		return err == nil && r.Status == handoverchild.StatusRunning
	}) {
		return fmt.Errorf("handover run %s is %q, want running", runID, run.Status)
	}
	files := []struct{ name, body string }{
		{handover.HandoverFile, md},
		{handover.MemoriesFile, memories},
		{handover.DoneFile, "done\n"},
	}
	for _, f := range files {
		if f.body == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(run.Dir, f.name), []byte(f.body), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// MemoryJobs reads the pending memory jobs for one session through the socket.
func (h *Harness) MemoryJobs(sessionID string) ([]memoryjob.Job, error) {
	resp, err := h.Call("memory.jobs", nil)
	if err != nil {
		return nil, err
	}
	body, ok := resp.Result.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("decode memory jobs: %T", resp.Result)
	}
	b, err := marshalJSON(body["jobs"])
	if err != nil {
		return nil, err
	}
	var all []memoryjob.Job
	if err := unmarshalJSON(b, &all); err != nil {
		return nil, err
	}
	var out []memoryjob.Job
	for _, j := range all {
		if j.SessionID == sessionID {
			out = append(out, j)
		}
	}
	return out, nil
}

// SteerPending is the session's undelivered steering notes.
func (h *Harness) SteerPending(sessionID string) []server.SteerNote {
	return h.srv.Steerer().Status(sessionID).Pending
}

func (h *Harness) closeHandovers() {
	if h.handovers != nil {
		h.handovers.Close()
	}
}
