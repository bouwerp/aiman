package harness

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/redact"
)

// ErrParams marks a request the run cannot take: an unknown check, step,
// result or service.
var ErrParams = errors.New("invalid harness request")

func paramsError(msg string) error { return fmt.Errorf("%w: %s", ErrParams, msg) }

// checkOutputLines is how much of a check's output a result keeps.
const checkOutputLines = 60

func (m *Manager) logf(format string, args ...any) {
	log.Printf("harness: "+format, args...)
}

// Down stops a live run: any start in flight ends, the compose project and
// its volumes go, and the run is kept as stopped with reason.
func (m *Manager) Down(ctx context.Context, id, reason string) error {
	m.mu.Lock()
	lr := m.live[id]
	m.mu.Unlock()
	if lr == nil {
		return &Error{Code: CodeNotRunning, Msg: fmt.Sprintf("harness run %s is not running", id)}
	}
	lr.mu.Lock()
	if lr.stopping {
		lr.mu.Unlock()
		return &Error{Code: CodeNotRunning, Msg: fmt.Sprintf("harness run %s is already stopping", id)}
	}
	lr.stopping = true
	cancel := lr.cancel
	lr.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	<-lr.done
	run := lr.snapshot()
	m.composeDown(ctx, run)
	m.markStopped(run, reason)
	m.release(lr)
	return nil
}

// composeDown saves each container's log under logs/services/, then removes
// the project and its volumes. It runs on its own deadline so a caller's
// cancelled context cannot leave containers behind.
func (m *Manager) composeDown(ctx context.Context, r Run) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopTimeout)
	defer cancel()
	dir := m.runDir(r.Session, r.ID)
	call := ComposeCall{Dir: filepath.Join(dir, "sandbox"), Project: "harness-" + r.ID}
	logDir := filepath.Join(dir, "logs", "services")
	if err := os.MkdirAll(logDir, 0o700); err == nil {
		for _, svc := range r.containers() {
			c := call
			c.Args = []string{"logs", "--no-color", "--no-log-prefix", svc}
			if out, _, err := m.opts.Docker.Compose(ctx, c); err == nil && out != "" {
				_ = os.WriteFile(filepath.Join(logDir, svc+".log"), []byte(out), 0o600)
			}
		}
	}
	call.Args = []string{"down", "-v", "--remove-orphans", "-t", "5"}
	if out, exit, err := m.opts.Docker.Compose(ctx, call); err != nil || exit != 0 {
		m.logf("compose down %s: exit %d %v: %s", call.Project, exit, err, tailLines(out, 5))
	}
}

// containers are the compose services of the run whose logs may be read.
func (r Run) containers() []string {
	return append([]string{ServiceApp, ServiceEdge}, r.Services...)
}

func (m *Manager) markStopped(r Run, reason string) {
	now := m.opts.Now()
	r.State, r.StopReason, r.StoppedAt = StateStopped, reason, &now
	if err := m.writeRun(r); err != nil {
		m.logf("write run %s: %v", r.ID, err)
	}
	m.record(r.Session, domain.EventActorAiman, domain.EventHarnessStopped, map[string]any{"run": r.ID, "reason": reason})
	m.prune(r.Session)
}

// readyRunOf returns the live run id when it is ready.
func (m *Manager) readyRunOf(id string) (*liveRun, error) {
	m.mu.Lock()
	lr := m.live[id]
	m.mu.Unlock()
	if lr == nil {
		return nil, &Error{Code: CodeNotRunning, Msg: fmt.Sprintf("harness run %s is not running", id)}
	}
	if st := lr.snapshot().State; st != StateReady {
		return nil, &Error{Code: CodeNotRunning, Msg: fmt.Sprintf("harness run %s is %s, not ready", id, st)}
	}
	return lr, nil
}

// Check runs the run's checks, or only the one named, each in a fresh app
// container on the inner network. Exit 0 is a pass.
func (m *Manager) Check(ctx context.Context, id, name string, actor domain.EventActor) ([]CheckRun, error) {
	lr, err := m.readyRunOf(id)
	if err != nil {
		return nil, err
	}
	run := lr.snapshot()
	var picked []CheckRun
	for _, c := range run.Checks {
		if name == "" || c.Name == name {
			picked = append(picked, c)
		}
	}
	if len(picked) == 0 {
		if name == "" {
			return nil, paramsError(fmt.Sprintf("scenario %s has no checks", run.Scenario))
		}
		return nil, paramsError(fmt.Sprintf("scenario %s has no check %q", run.Scenario, name))
	}
	call := ComposeCall{Dir: filepath.Join(m.runDir(run.Session, run.ID), "sandbox"), Project: "harness-" + run.ID}
	for i, c := range picked {
		cctx, cancel := context.WithTimeout(ctx, CheckTimeout)
		cc := call
		cc.Args = []string{"run", "--rm", "--no-deps", ServiceApp, "sh", "-c", c.Run}
		out, exit, err := m.opts.Docker.Compose(cctx, cc)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("check %s: %w", c.Name, err)
		}
		at := m.opts.Now()
		lines, _ := redact.Lines(strings.Split(tailLines(out, checkOutputLines), "\n"), nil)
		picked[i].Exit, picked[i].At, picked[i].Output = &exit, &at, strings.Join(lines, "\n")
		m.record(run.Session, actor, domain.EventHarnessCheck, map[string]any{"run": run.ID, "name": c.Name, "exit": exit})
	}
	lr.mu.Lock()
	for _, p := range picked {
		for j := range lr.run.Checks {
			if lr.run.Checks[j].Name == p.Name {
				lr.run.Checks[j] = p
			}
		}
	}
	lr.run.LastUsed = m.opts.Now()
	run = lr.run
	lr.mu.Unlock()
	if err := m.writeRun(run); err != nil {
		return nil, err
	}
	return picked, nil
}

// Verdict records a tester's result for step (1-based; 0 is the whole
// scenario) of a live or kept run. The verdict carries the run's sha, dirty
// flag and diff hash.
func (m *Manager) Verdict(id string, step int, result, note, by string) (Verdict, Run, error) {
	if !slices.Contains([]string{ResultPass, ResultFail, ResultSkip}, result) {
		return Verdict{}, Run{}, paramsError("result must be pass, fail or skip")
	}
	m.mu.Lock()
	lr := m.live[id]
	m.mu.Unlock()
	var run Run
	if lr != nil {
		lr.mu.Lock()
		defer lr.mu.Unlock()
		run = lr.run
	} else {
		r, err := m.Get(id)
		if err != nil {
			return Verdict{}, Run{}, err
		}
		run = r
	}
	if step < 0 || step > len(run.Steps) {
		return Verdict{}, Run{}, paramsError(fmt.Sprintf("step %d is not 0 (the scenario) or 1-%d", step, len(run.Steps)))
	}
	now := m.opts.Now()
	v := Verdict{RunID: run.ID, Step: step, Result: result, Note: strings.TrimSpace(note), By: by, SHA: run.SHA, Dirty: run.Dirty, DiffSHA256: run.DiffSHA256, At: now}
	if step == 0 {
		run.Result = result
	} else {
		run.Steps[step-1].Result, run.Steps[step-1].Note = result, v.Note
	}
	run.Verdicts = append(run.Verdicts, v)
	run.LastUsed = now
	if err := m.writeRun(run); err != nil {
		return Verdict{}, Run{}, err
	}
	if lr != nil {
		lr.run = run
	}
	m.record(run.Session, domain.EventActorUser, domain.EventHarnessVerdict, map[string]any{
		"run": run.ID, "scenario": run.Scenario, "step": step, "result": result, "note": v.Note,
		"sha": run.SHA, "dirty": run.Dirty, "diff_sha256": run.DiffSHA256,
	})
	return v, run, nil
}

// Logs returns the last lines of one container's output, unredacted: the
// live log of a running run, else the log saved when it stopped.
func (m *Manager) Logs(ctx context.Context, id, service string, lines int) ([]string, error) {
	run, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(run.containers(), service) {
		return nil, paramsError(fmt.Sprintf("run %s has no service %q; it has %s", id, service, strings.Join(run.containers(), ", ")))
	}
	var out string
	if run.Live() {
		m.Touch(id)
		var exit int
		out, exit, err = m.opts.Docker.Compose(ctx, ComposeCall{
			Dir: filepath.Join(m.runDir(run.Session, run.ID), "sandbox"), Project: "harness-" + run.ID,
			Args: []string{"logs", "--no-color", "--no-log-prefix", "--tail", strconv.Itoa(lines), service},
		})
		if err != nil {
			return nil, err
		}
		if exit != 0 {
			return nil, fmt.Errorf("compose logs %s exited %d: %s", service, exit, tailLines(out, 3))
		}
	} else {
		b, rerr := os.ReadFile(filepath.Join(m.runDir(run.Session, run.ID), "logs", "services", service+".log")) //nolint:gosec // G304: service is one of the run's containers
		if rerr != nil {
			return []string{}, nil //nolint:nilerr // a container that wrote nothing has no saved log
		}
		out = tailLines(string(b), lines)
	}
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return []string{}, nil
	}
	return strings.Split(out, "\n"), nil
}
