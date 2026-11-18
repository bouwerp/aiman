package harness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/harness/edge"
)

// Debug check refusals and results.
const (
	// CodeDebugBusy refuses a debug check while the run's debugger serves
	// another target: a debug check that still runs, or a client holding
	// the app's debugger (unless the call takes it over).
	CodeDebugBusy = "harness_debug_busy"
	// CodeDebugTimeout ends a debug check no debugger attached to within
	// harness.debug.attach_timeout. It is the check result's error_code.
	CodeDebugTimeout = "harness_debug_timeout"
)

// debugCheckStopWait bounds the wait for a stopped check container's
// compose wait to return.
const debugCheckStopWait = 30 * time.Second

// DebugCheckRequest asks for one check under its debugger. Take closes a
// client holding the app's debugger instead of refusing.
type DebugCheckRequest struct {
	Run   string
	Name  string
	Take  bool
	Actor domain.EventActor
}

// debugCheck is a check that runs, or is being started, under the
// debugger. cancel ends ctx, its watcher's (harness.down, serve closing),
// and done closes once the watcher, or a failed start, is finished with the
// run.
type debugCheck struct {
	name   string
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// stop ends the watcher and waits for it.
func (dc *debugCheck) stop() {
	dc.cancel()
	<-dc.done
}

// debugCheckRun is what a debug check's start and watcher share.
type debugCheckRun struct {
	lr      *liveRun
	dc      *debugCheck
	run     Run
	token   string
	sock    string
	call    ComposeCall
	base    RunSpec
	name    string
	lang    string
	index   int
	actor   domain.EventActor
	attach  time.Duration
	started bool
}

// DebugCheck runs one check of a ready debug run as the run's debug target:
// in a fresh app container on the debug network at the check's address
// (DebugCheckAddrs), under its debug block's adapter, with the DAP relay
// pointed at it. It returns once the check container has started; the
// check's result lands in the run's checks when it ends, and the relay
// points back at the app.
//
// A run has one debug target at a time. A second debug check while one
// runs is refused with CodeDebugBusy (target check:<name>), and so is one
// while a client holds the app's debugger, unless Take is set: then that
// client is closed. A running debug check is never taken over; it ends when
// its process exits, at harness.down, or when no debugger attached within
// the attach timeout.
func (m *Manager) DebugCheck(ctx context.Context, req DebugCheckRequest) (CheckRun, error) {
	lr, err := m.readyRunOf(req.Run)
	if err != nil {
		return CheckRun{}, err
	}
	run := lr.snapshot()
	if !run.Debug {
		return CheckRun{}, paramsError(fmt.Sprintf("run %s is not a debug run; start it with harness up --debug (or --debug-checks) to debug a check", run.ID))
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return CheckRun{}, paramsError("name the check to debug")
	}
	lr.mu.Lock()
	f, base, token := lr.file, lr.spec, lr.token
	lr.mu.Unlock()
	if f == nil {
		return CheckRun{}, fmt.Errorf("harness run %s has no compose project", run.ID)
	}
	check, j, err := scenarioCheck(f, run.Scenario, name)
	if err != nil {
		return CheckRun{}, err
	}
	cf := CheckDebugFile(f, check)
	adapter, err := CheckAdapterFor(cf, m.opts.DebugAdapters, name)
	if err != nil {
		return CheckRun{}, err
	}
	dir := m.runDir(run.Session, run.ID)
	w := &debugCheckRun{
		lr: lr, run: run, token: token, base: base, name: name, index: j, lang: check.Debug.Lang, actor: req.Actor,
		sock:   filepath.Join(dir, "control", ControlSocketName),
		call:   ComposeCall{Dir: filepath.Join(dir, "sandbox"), Project: "harness-" + run.ID},
		attach: m.opts.DebugAttachTimeout,
	}
	if err := m.claimDebugCheck(w); err != nil {
		return CheckRun{}, err
	}
	defer func() {
		if !w.started {
			lr.mu.Lock()
			if lr.check == w.dc {
				lr.check = nil
			}
			lr.mu.Unlock()
			w.dc.cancel()
			close(w.dc.done)
		}
	}()
	// The start ends early when harness.down or serve's close cancels it.
	sctx, scancel := context.WithCancel(ctx)
	defer scancel()
	stopStart := context.AfterFunc(w.dc.ctx, scancel)
	defer stopStart()

	h, err := m.opts.Edge.Health(sctx, w.sock, token)
	if err != nil {
		return CheckRun{}, fmt.Errorf("edge: %w", err)
	}
	if holder := dapHolder(h); holder != nil && !req.Take {
		return CheckRun{}, &Error{Code: CodeDebugBusy, Target: TargetApp,
			Msg: fmt.Sprintf("the app's debugger is held by %s; take it over (take, --take, or the console's Debug check) or release it first", holderName(holder))}
	}
	spec, err := adapter.Spec(sctx, cf)
	if err != nil {
		return CheckRun{}, err
	}
	launch, err := adapter.Launch(cf, DebugCheckNetOf(base.DebugSubnet), spec)
	if err != nil {
		return CheckRun{}, err
	}
	cr, err := m.startDebugCheck(sctx, w, f, spec, &launch)
	if err != nil {
		return CheckRun{}, err
	}
	return cr, nil
}

// claimDebugCheck makes w's check the run's debug check, or refuses.
func (m *Manager) claimDebugCheck(w *debugCheckRun) error {
	cctx, cancel := context.WithCancel(context.Background())
	dc := &debugCheck{name: w.name, ctx: cctx, cancel: cancel, done: make(chan struct{})}
	lr := w.lr
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if lr.stopping {
		cancel()
		return &Error{Code: CodeNotRunning, Msg: fmt.Sprintf("harness run %s is stopping", w.run.ID)}
	}
	if cur := lr.check; cur != nil {
		cancel()
		return &Error{Code: CodeDebugBusy, Target: TargetCheck + ":" + cur.name,
			Msg: fmt.Sprintf("check %s is under the debugger; a run debugs one target at a time, so wait for it to end", cur.name)}
	}
	lr.check, w.dc = dc, dc
	return nil
}

// startDebugCheck writes the compose project with the check's services,
// probes, starts the check's adapter, points the relay at the check and
// starts it, then hands it to the watcher.
func (m *Manager) startDebugCheck(ctx context.Context, w *debugCheckRun, f *File, spec *DebugSpec, launch *DebugLaunch) (CheckRun, error) {
	rs := w.base
	rs.CheckDebug, rs.CheckAdapterImage = spec, w.run.Images[ServiceCheckAdapter]
	// A check that names an image is debugged in that image.
	rs.CheckDebugImage = w.run.Images[CheckService(w.index)]
	if spec.Adapter != nil && rs.CheckAdapterImage == "" {
		return CheckRun{}, fmt.Errorf("check %s needs the debug adapter image, which this run did not vet; take the run down and start it again", w.name)
	}
	p, err := Compose(f, rs)
	if err != nil {
		return CheckRun{}, err
	}
	if err := os.WriteFile(filepath.Join(w.call.Dir, "compose.yaml"), p.YAML, 0o600); err != nil {
		return CheckRun{}, err
	}
	m.removeDebugCheck(ctx, w)
	if spec.Probe != nil {
		out, exit, err := m.compose(ctx, w.call, append([]string{"run", "--rm", "--no-deps", ServiceDebugCheck}, spec.Probe.Command...)...)
		if err != nil || exit != 0 {
			return CheckRun{}, fmt.Errorf("debug probe of check %s exited %d %v: %s", w.name, exit, err, tailLines(out, 5))
		}
		if err := spec.Probe.Check(out); err != nil {
			return CheckRun{}, err
		}
	}
	fail := func(err error) (CheckRun, error) {
		m.removeDebugCheck(ctx, w)
		m.debugTargetApp(w)
		return CheckRun{}, err
	}
	if spec.Adapter != nil {
		if out, exit, err := m.compose(ctx, w.call, "up", "-d", "--no-deps", ServiceCheckAdapter); err != nil || exit != 0 {
			return fail(fmt.Errorf("start the debug adapter of check %s: exit %d %v: %s", w.name, exit, err, tailLines(out, 5)))
		}
	}
	if _, err := m.opts.Edge.DAPTarget(ctx, w.sock, w.token, RelayDAP, CheckDAPTarget(rs)); err != nil {
		return fail(fmt.Errorf("edge: %w", err))
	}
	if out, exit, err := m.compose(ctx, w.call, "up", "-d", "--no-deps", ServiceDebugCheck); err != nil || exit != 0 {
		return fail(fmt.Errorf("start check %s: exit %d %v: %s", w.name, exit, err, tailLines(out, 5)))
	}
	now := m.opts.Now()
	state := &DebugCheck{Name: w.name, Lang: w.lang, State: DebugCheckWaiting, Launch: launch, StartedAt: now, AttachBy: now.Add(w.attach)}
	lr := w.lr
	lr.mu.Lock()
	if lr.stopping {
		lr.mu.Unlock()
		return fail(&Error{Code: CodeNotRunning, Msg: fmt.Sprintf("harness run %s is stopping", w.run.ID)})
	}
	var cr CheckRun
	// A copy: snapshots of the run share the old slice.
	lr.run.Checks = slices.Clone(lr.run.Checks)
	for i := range lr.run.Checks {
		if lr.run.Checks[i].Name == w.name {
			c := &lr.run.Checks[i]
			c.Exit, c.At, c.Output, c.ErrorCode, c.Error = nil, nil, "", "", ""
			c.Debug, c.Debugging = true, true
			cr = *c
		}
	}
	lr.run.DebugCheck, lr.run.LastUsed = state, now
	run := lr.run
	lr.mu.Unlock()
	if err := m.writeRun(run); err != nil {
		m.logf("write run %s: %v", run.ID, err)
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return fail(errors.New("harness manager is closed"))
	}
	m.wg.Add(1)
	m.mu.Unlock()
	w.started = true
	go m.followDebugCheck(w)
	return cr, nil
}

// waitResult is compose wait's answer: the check container's exit code.
type waitResult struct {
	out  string
	exit int
	err  error
}

// followDebugCheck watches a started debug check: it marks the check
// attached once a debugger client holds the relay, stops it when nobody
// attached within the attach timeout, and on its end records the result
// and points the relay back at the app.
func (m *Manager) followDebugCheck(w *debugCheckRun) {
	defer m.wg.Done()
	defer close(w.dc.done)
	ctx := w.dc.ctx
	waited := make(chan waitResult, 1)
	go func() {
		out, exit, err := m.opts.Docker.Compose(ctx, ComposeCall{Dir: w.call.Dir, Project: w.call.Project, Args: []string{"wait", ServiceDebugCheck}})
		waited <- waitResult{out: out, exit: exit, err: err}
	}()
	attach := time.NewTimer(w.attach)
	defer attach.Stop()
	poll := time.NewTicker(m.opts.DebugPoll)
	defer poll.Stop()
	var giveUp <-chan time.Time
	attached, timedOut := false, false
	var res waitResult
loop:
	for {
		select {
		case res = <-waited:
			break loop
		case <-poll.C:
			m.Touch(w.run.ID)
			if !attached && m.debugClient(ctx, w) {
				attached = true
				attach.Stop()
				w.lr.mu.Lock()
				if cur := w.lr.run.DebugCheck; cur != nil {
					// A copy: snapshots of the run share the old one.
					next := *cur
					next.State = DebugCheckAttached
					w.lr.run.DebugCheck = &next
				}
				w.lr.mu.Unlock()
			}
		case <-attach.C:
			if attached {
				continue
			}
			timedOut = true
			m.removeDebugCheck(ctx, w)
			giveUp = time.After(debugCheckStopWait)
		case <-giveUp:
			break loop
		case <-ctx.Done():
			// harness.down or serve closing: compose down, or the next
			// serve's CleanStale, removes the containers.
			m.clearDebugCheck(w, nil)
			return
		}
	}
	if ctx.Err() != nil {
		m.clearDebugCheck(w, nil)
		return
	}
	m.finishDebugCheck(w, res, timedOut)
}

// debugClient reports whether a debugger client holds the DAP relay.
func (m *Manager) debugClient(ctx context.Context, w *debugCheckRun) bool {
	hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	h, err := m.opts.Edge.Health(hctx, w.sock, w.token)
	return err == nil && dapHolder(h) != nil
}

// finishDebugCheck reads the ended check's output, removes its containers,
// points the relay back at the app and records the result.
func (m *Manager) finishDebugCheck(w *debugCheckRun, res waitResult, timedOut bool) {
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	out, _, _ := m.compose(ctx, w.call, "logs", "--no-color", "--no-log-prefix", ServiceDebugCheck)
	m.removeDebugCheck(ctx, w)
	m.debugTargetApp(w)
	at := m.opts.Now()
	c := CheckRun{Output: checkOutput(out), At: &at, Debug: true}
	switch {
	case timedOut:
		c.ErrorCode = CodeDebugTimeout
		c.Error = fmt.Sprintf("no debugger attached to check %s within %s; it was stopped", w.name, w.attach)
	case res.err != nil || !strings.Contains(res.out, "exit"):
		c.Error = fmt.Sprintf("check %s ended without an exit code: %v %s", w.name, res.err, tailLines(res.out, 3))
	default:
		exit := res.exit
		c.Exit = &exit
	}
	payload := map[string]any{"run": w.run.ID, "name": w.name, "debug": true, "lang": w.lang}
	if c.Exit != nil {
		payload["exit"] = *c.Exit
	}
	if c.ErrorCode != "" {
		payload["error_code"] = c.ErrorCode
	}
	// The ledger row goes in before the run shows the result, so a caller
	// that waits for the result finds the row.
	m.record(w.run.Session, w.actor, domain.EventHarnessCheck, payload)
	m.clearDebugCheck(w, &c)
}

// clearDebugCheck ends the run's debug check: the check's entry takes
// result (nil keeps the last one) and the debugger serves the app again.
func (m *Manager) clearDebugCheck(w *debugCheckRun, result *CheckRun) {
	lr := w.lr
	lr.mu.Lock()
	if lr.check == w.dc {
		lr.check = nil
	}
	lr.run.DebugCheck = nil
	lr.run.Checks = slices.Clone(lr.run.Checks)
	for i := range lr.run.Checks {
		c := &lr.run.Checks[i]
		if c.Name != w.name {
			continue
		}
		c.Debugging = false
		if result != nil {
			c.Exit, c.At, c.Output, c.Debug, c.ErrorCode, c.Error = result.Exit, result.At, result.Output, true, result.ErrorCode, result.Error
		}
	}
	run, stopping := lr.run, lr.stopping
	lr.mu.Unlock()
	if !stopping {
		if err := m.writeRun(run); err != nil {
			m.logf("write run %s: %v", run.ID, err)
		}
	}
}

// removeDebugCheck stops and removes the check's container and its adapter.
func (m *Manager) removeDebugCheck(ctx context.Context, w *debugCheckRun) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	if out, exit, err := m.compose(ctx, w.call, "rm", "-s", "-f", ServiceDebugCheck, ServiceCheckAdapter); err != nil || exit != 0 {
		m.logf("remove the debug check of %s: exit %d %v: %s", w.run.ID, exit, err, tailLines(out, 3))
	}
}

// debugTargetApp points the relay back at the app's debugger (or at
// nothing in a run that debugs only its checks).
func (m *Manager) debugTargetApp(w *debugCheckRun) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := m.opts.Edge.DAPTarget(ctx, w.sock, w.token, RelayDAP, AppDAPTarget(w.base)); err != nil {
		m.logf("point the debugger of %s back at the app: %v", w.run.ID, err)
	}
}

func (m *Manager) compose(ctx context.Context, call ComposeCall, args ...string) (string, int, error) {
	call.Args = args
	return m.opts.Docker.Compose(ctx, call)
}

// scenarioCheck finds check name of scenario in f, which needs a debug
// block.
func scenarioCheck(f *File, scenario, name string) (Check, int, error) {
	sc, ok := f.Scenario(scenario)
	if !ok {
		return Check{}, 0, paramsError(fmt.Sprintf("no scenario %q in the run's harness file", scenario))
	}
	for j, c := range sc.Checks {
		if c.Name != name {
			continue
		}
		if c.Debug == nil {
			return Check{}, 0, paramsError(fmt.Sprintf("check %q has no debug block; give it debug: {lang: node, run: ...} in the harness file", name))
		}
		return c, j, nil
	}
	return Check{}, 0, paramsError(fmt.Sprintf("scenario %s has no check %q", scenario, name))
}

// hasDebugChecks reports whether sc has a check with a debug block.
func hasDebugChecks(sc *Scenario) bool {
	for _, c := range sc.Checks {
		if c.Debug != nil {
			return true
		}
	}
	return false
}

// checkAdapterImage is the adapter image a debug check of sc may start, so
// harness.up vets it with the run's images; empty when none needs one.
func (m *Manager) checkAdapterImage(sc *Scenario) string {
	for _, c := range sc.Checks {
		if c.Debug == nil {
			continue
		}
		for _, a := range m.opts.DebugAdapters {
			if a.Lang() == c.Debug.Lang && a.AdapterImage() != "" {
				return a.AdapterImage()
			}
		}
	}
	return ""
}

// dapHolder is the client holding the DAP relay in h, nil for none.
func dapHolder(h edge.Health) *edge.DAPHolder {
	for _, r := range h.Relays {
		if r.Name == RelayDAP {
			return r.Holder
		}
	}
	return nil
}

func holderName(h *edge.DAPHolder) string {
	switch {
	case h.ClientName != "":
		return h.ClientName
	case h.ClientID != "":
		return h.ClientID
	}
	return "another client"
}
