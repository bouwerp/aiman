package harness

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/harness/edge"
)

// debugCheckFile is a Go app with two checks: a Vitest check that runs
// under the debugger on Node, and a plain one.
const debugCheckFile = `version: 1
app:
  build: go build -o /out/app .
  run: /out/app serve --addr :8080
  ports: [{name: web, container: 8080}]
  debug: {lang: go}
scenarios:
  - id: plain
    checks:
      - name: orders
        run: bunx vitest run tests/orders.test.ts
        debug:
          lang: node
          run: node node_modules/vitest/vitest.mjs run --inspect-brk=$AIMAN_INSPECT --no-file-parallelism --test-timeout=0 tests/orders.test.ts
      - name: unit
        run: go test ./...
        debug: {lang: go, run: dlv test --listen=$AIMAN_INSPECT}
      - name: lint
        run: "true"
`

// checkDocker answers compose wait for the debug check only when the test
// releases it with an exit code, or when the check container is removed.
type checkDocker struct {
	*fakeDocker
	release chan int
}

func newCheckDocker(tm *testManager) *checkDocker {
	cd := &checkDocker{fakeDocker: tm.docker, release: make(chan int, 4)}
	var waiting atomic.Int32
	tm.docker.answer = func(args []string) (string, int, bool) {
		switch {
		case len(args) >= 2 && args[0] == "wait" && args[1] == ServiceDebugCheck:
			waiting.Add(1)
			defer waiting.Add(-1)
			select {
			case code := <-cd.release:
				return "container x exited with status code " + strconv.Itoa(code), code, true
			case <-time.After(10 * time.Second):
				return "timed out in the test", 1, true
			}
		case len(args) >= 1 && args[0] == "rm":
			// Removing a running check container ends its wait.
			if waiting.Load() > 0 {
				select {
				case cd.release <- 137:
				default:
				}
			}
		case len(args) >= 1 && args[0] == "logs":
			return "Debugger listening on ws://10.232.0.5:9229\n 1 passed\n", 0, true
		}
		return "", 0, false
	}
	tm.docker.mu.Lock()
	tm.docker.images[AdapterImage] = "sha256:" + strings.Repeat("a", 64)
	tm.docker.mu.Unlock()
	return cd
}

func newDebugCheckManager(t *testing.T, he *holdingEdge, mut ...func(*Options)) (*testManager, *checkDocker, Run) {
	t.Helper()
	tm := newTestManager(t, append([]func(*Options){func(o *Options) {
		o.Edge = he
		o.DebugAdapters = []DebugAdapter{&stubGoDebug{}, &stubNodeDebug{}}
		o.DebugPoll = 10 * time.Millisecond
	}}, mut...)...)
	cd := newCheckDocker(tm)
	repo := testRepo(t, debugCheckFile)
	run, err := tm.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: "plain", Debug: true, Actor: domain.EventActorUser})
	if err != nil {
		t.Fatal(err)
	}
	run = tm.Wait(context.Background(), run.ID, 5*time.Second)
	if run.State != StateReady {
		t.Fatalf("run %s: %s %s", run.State, run.FailedStep, run.Error)
	}
	return tm, cd, run
}

// waitRun polls the run until ok holds.
func waitRun(t *testing.T, tm *testManager, id string, ok func(Run) bool) Run {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, err := tm.Get(id)
		if err == nil && ok(r) {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s never got there: %+v", id, r)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func ordersCheck(r Run) CheckRun {
	for _, c := range r.Checks {
		if c.Name == "orders" {
			return c
		}
	}
	return CheckRun{}
}

func debugSubnetOf(t *testing.T, tm *testManager, run Run) netip.Prefix {
	t.Helper()
	tm.mu.Lock()
	lr := tm.live[run.ID]
	tm.mu.Unlock()
	lr.mu.Lock()
	defer lr.mu.Unlock()
	return lr.spec.DebugSubnet
}

// TestDebugCheckNodeOnPath runs a Vitest check under the debugger: its own
// container at the check's address with the mounted Node first on PATH,
// js-debug in a second adapter container, the relay switched to it, and
// back to the app's debugger when the check ends.
func TestDebugCheckNodeOnPath(t *testing.T) {
	he := &holdingEdge{}
	tm, cd, run := newDebugCheckManager(t, he)
	if c := ordersCheck(run); c.DebugLang != "node" {
		t.Fatalf("check entry %+v, want debug_lang node", c)
	}
	cr, err := tm.DebugCheck(context.Background(), DebugCheckRequest{Run: run.ID, Name: "orders", Actor: domain.EventActorUser})
	if err != nil {
		t.Fatal(err)
	}
	if !cr.Debugging || !cr.Debug || cr.Exit != nil {
		t.Fatalf("started check %+v", cr)
	}
	subnet := debugSubnetOf(t, tm, run)
	check, adapter := DebugCheckAddrs(subnet)
	app, _, _ := DebugAddrs(subnet)

	yml, err := os.ReadFile(filepath.Join(tm.dir, "s1", run.ID, "sandbox", "compose.yaml")) //nolint:gosec // G304: the test's run directory
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		ServiceDebugCheck + ":", ServiceCheckAdapter + ":", ServiceCheck + ":",
		// compose sees $ doubled; the container's shell sees one.
		`else PATH="` + NodeToolMount + `/bin:$$PATH"; fi`,
		"ipv4_address: " + check.String(),
		InspectEnv + ": " + check.String() + ":9229",
		AdapterHostEnv + ": " + adapter.String(),
		"--inspect-brk=$$AIMAN_INSPECT",
	} {
		if !strings.Contains(string(yml), want) {
			t.Errorf("compose file lacks %q", want)
		}
	}
	if t.Failed() {
		t.Fatalf("compose file:\n%s", yml)
	}
	// called counts by prefix: the check and its adapter.
	if tm.docker.called("up -d --no-deps "+ServiceCheckAdapter) != 1 || tm.docker.called("up -d --no-deps "+ServiceDebugCheck) != 2 {
		t.Fatalf("check or adapter not started; calls %q", tm.docker.calls)
	}
	if got := he.targetsSeen(); len(got) != 1 || got[0] != adapter.String()+":"+strconv.Itoa(JSDebugPort) {
		t.Fatalf("relay targets %v, want the check's adapter", got)
	}
	r, _ := tm.Get(run.ID)
	dbg := r.DebugCheck
	if dbg == nil || dbg.Name != "orders" || dbg.State != DebugCheckWaiting || dbg.Launch == nil ||
		dbg.Launch.Arguments["address"] != check.String() || r.TargetLaunch() != dbg.Launch {
		t.Fatalf("debug check %+v", dbg)
	}
	st, err := tm.Debug(context.Background(), run.ID, DebugStatus, domain.EventActorUser)
	if err != nil || st.Target != TargetCheck || st.Check != "orders" || st.Launch != dbg.Launch {
		t.Fatalf("debug status %+v %v", st, err)
	}

	he.setHolder(&edge.DAPHolder{ClientName: "aiman harness console", Since: time.Now()})
	waitRun(t, tm, run.ID, func(r Run) bool { return r.DebugCheck != nil && r.DebugCheck.State == DebugCheckAttached })

	cd.release <- 0
	r = waitRun(t, tm, run.ID, func(r Run) bool { return r.DebugCheck == nil })
	c := ordersCheck(r)
	if c.Exit == nil || *c.Exit != 0 || !c.Debug || c.Debugging || !strings.Contains(c.Output, "1 passed") || c.ErrorCode != "" {
		t.Fatalf("check result %+v", c)
	}
	if got := he.targetsSeen(); got[len(got)-1] != app.String()+":"+strconv.Itoa(goDAPPort) {
		t.Fatalf("relay targets %v, want the app's debugger last", got)
	}
	if tm.docker.called("rm -s -f "+ServiceDebugCheck) < 1 {
		t.Fatalf("check container not removed; calls %q", tm.docker.calls)
	}
	p := tm.rec.last(domain.EventHarnessCheck)
	if p["debug"] != true || p["exit"] != 0 || p["name"] != "orders" {
		t.Fatalf("harness.check = %v", p)
	}
	st, err = tm.Debug(context.Background(), run.ID, DebugStatus, domain.EventActorUser)
	if err != nil || st.Target != TargetApp || st.Launch != r.DebugLaunch {
		t.Fatalf("debug status after the check %+v %v", st, err)
	}

	// Plain checks of a debug run use the check service, whose container
	// is not on the debug network the app's address is on.
	if _, err := tm.Check(context.Background(), run.ID, "lint", domain.EventActorUser); err != nil {
		t.Fatal(err)
	}
	if tm.docker.called("run --rm --no-deps "+ServiceCheck+" sh -c true") != 1 {
		t.Fatalf("plain check calls %q", tm.docker.calls)
	}
	if p := tm.rec.last(domain.EventHarnessCheck); p["debug"] != false {
		t.Fatalf("plain harness.check = %v", p)
	}
}

// TestDebugCheckBusy: one debug target at a time.
func TestDebugCheckBusy(t *testing.T) {
	he := &holdingEdge{holder: &edge.DAPHolder{ClientName: "Visual Studio Code", Since: time.Now()}}
	tm, cd, run := newDebugCheckManager(t, he)
	req := DebugCheckRequest{Run: run.ID, Name: "orders", Actor: domain.EventActorUser}

	// A client holds the app's debugger: refused unless taken over.
	_, err := tm.DebugCheck(context.Background(), req)
	var he2 *Error
	if !errors.As(err, &he2) || he2.Code != CodeDebugBusy || he2.Data()["target"] != TargetApp || !strings.Contains(he2.Msg, "Visual Studio Code") {
		t.Fatalf("held app debugger: %v", err)
	}
	if tm.docker.called("up -d --no-deps "+ServiceDebugCheck) != 0 {
		t.Fatal("the check started although the debugger was busy")
	}
	req.Take = true
	if _, err := tm.DebugCheck(context.Background(), req); err != nil {
		t.Fatalf("take over: %v", err)
	}
	// A second debug check waits for the first; take does not end it.
	_, err = tm.DebugCheck(context.Background(), req)
	if !errors.As(err, &he2) || he2.Code != CodeDebugBusy || he2.Data()["target"] != "check:orders" {
		t.Fatalf("second debug check: %v", err)
	}
	cd.release <- 1
	r := waitRun(t, tm, run.ID, func(r Run) bool { return r.DebugCheck == nil })
	if c := ordersCheck(r); c.Exit == nil || *c.Exit != 1 {
		t.Fatalf("check result %+v", c)
	}
	// Free again: the next debug check starts.
	if _, err := tm.DebugCheck(context.Background(), req); err != nil {
		t.Fatalf("after the first ended: %v", err)
	}
	cd.release <- 0
	waitRun(t, tm, run.ID, func(r Run) bool { return r.DebugCheck == nil })
}

// TestDebugCheckTimeout stops a check no debugger attached to.
func TestDebugCheckTimeout(t *testing.T) {
	he := &holdingEdge{}
	tm, _, run := newDebugCheckManager(t, he, func(o *Options) { o.DebugAttachTimeout = 80 * time.Millisecond })
	cr, err := tm.DebugCheck(context.Background(), DebugCheckRequest{Run: run.ID, Name: "orders", Actor: domain.EventActorUser})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := tm.Get(run.ID)
	if r.DebugCheck == nil || !r.DebugCheck.AttachBy.Equal(r.DebugCheck.StartedAt.Add(80*time.Millisecond)) || !cr.Debugging {
		t.Fatalf("debug check %+v", r.DebugCheck)
	}
	r = waitRun(t, tm, run.ID, func(r Run) bool { return r.DebugCheck == nil })
	c := ordersCheck(r)
	if c.ErrorCode != CodeDebugTimeout || c.Exit != nil || !strings.Contains(c.Error, "no debugger attached") || c.Debugging {
		t.Fatalf("check result %+v", c)
	}
	if tm.docker.called("rm -s -f "+ServiceDebugCheck) < 2 {
		t.Fatalf("the check was not stopped; calls %q", tm.docker.calls)
	}
	p := tm.rec.last(domain.EventHarnessCheck)
	if p["debug"] != true || p["error_code"] != CodeDebugTimeout {
		t.Fatalf("harness.check = %v", p)
	}
	app, _, _ := DebugAddrs(debugSubnetOf(t, tm, run))
	if got := he.targetsSeen(); got[len(got)-1] != app.String()+":"+strconv.Itoa(goDAPPort) {
		t.Fatalf("relay targets %v", got)
	}
}

// TestDebugCheckDown: harness.down ends a debug check's watcher and takes
// the check container down with every profile.
func TestDebugCheckDown(t *testing.T) {
	tm, _, run := newDebugCheckManager(t, &holdingEdge{})
	if _, err := tm.DebugCheck(context.Background(), DebugCheckRequest{Run: run.ID, Name: "orders"}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- tm.Down(context.Background(), run.ID, StopDown) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("down hung on the debug check")
	}
	r, _ := tm.Get(run.ID)
	if r.State != StateStopped || r.DebugCheck != nil || ordersCheck(r).Debugging {
		t.Fatalf("stopped run %+v", r)
	}
	if tm.docker.called("--profile * down -v") != 1 {
		t.Fatalf("calls %q", tm.docker.calls)
	}
}

func TestDebugCheckRefusals(t *testing.T) {
	he := &holdingEdge{}
	tm, _, run := newDebugCheckManager(t, he)
	var e *Error
	for name, tc := range map[string]struct {
		req  DebugCheckRequest
		want func(error) bool
	}{
		"no name":       {DebugCheckRequest{Run: run.ID}, func(err error) bool { return errors.Is(err, ErrParams) }},
		"unknown check": {DebugCheckRequest{Run: run.ID, Name: "nope"}, func(err error) bool { return errors.Is(err, ErrParams) }},
		"no debug":      {DebugCheckRequest{Run: run.ID, Name: "lint"}, func(err error) bool { return errors.Is(err, ErrParams) }},
		"go check": {DebugCheckRequest{Run: run.ID, Name: "unit"}, func(err error) bool {
			return errors.As(err, &e) && e.Code == CodeDebugUnsupported && e.Reason == ReasonCheckLang
		}},
		"no run": {DebugCheckRequest{Run: "nope", Name: "orders"}, func(err error) bool { return errors.As(err, &e) && e.Code == CodeNotRunning }},
	} {
		if _, err := tm.DebugCheck(context.Background(), tc.req); !tc.want(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A plain run cannot debug a check.
	plain := newTestManager(t, func(o *Options) { o.DebugAdapters = []DebugAdapter{&stubNodeDebug{}} })
	pr := plain.upReady(t, testRepo(t, debugCheckFile), "plain")
	if _, err := plain.DebugCheck(context.Background(), DebugCheckRequest{Run: pr.ID, Name: "orders"}); !errors.Is(err, ErrParams) || !strings.Contains(err.Error(), "not a debug run") {
		t.Fatalf("plain run: %v", err)
	}
}

// TestDebugChecksOnlyRun: a debug run of a file whose app has no debugger
// debugs only its checks; the relay starts with no target.
func TestDebugChecksOnlyRun(t *testing.T) {
	file := strings.Replace(debugCheckFile, "  debug: {lang: go}\n", "", 1)
	tm := newTestManager(t, func(o *Options) {
		o.Edge = &holdingEdge{}
		o.DebugAdapters = []DebugAdapter{&stubNodeDebug{}}
	})
	newCheckDocker(tm)
	run, err := tm.Up(context.Background(), UpRequest{Session: "s1", Repo: testRepo(t, file), Scenario: "plain", Debug: true})
	if err != nil {
		t.Fatal(err)
	}
	run = tm.Wait(context.Background(), run.ID, 5*time.Second)
	if run.State != StateReady || !run.Debug || !run.DebugChecks || run.DebugLaunch != nil {
		t.Fatalf("run %+v", run)
	}
	yml, err := os.ReadFile(filepath.Join(tm.dir, "s1", run.ID, "sandbox", "compose.yaml")) //nolint:gosec // G304: the test's run directory
	if err != nil || !strings.Contains(string(yml), "--dap-relay\n            - dap=10002=\n") {
		t.Fatalf("compose file lacks the empty debugger relay: %v\n%s", err, yml)
	}
	st, err := tm.Debug(context.Background(), run.ID, DebugStatus, domain.EventActorUser)
	if err != nil || st.Target != "" || st.Launch != nil {
		t.Fatalf("status %+v %v", st, err)
	}
	// The scenario without debug checks is refused.
	if _, err := tm.Up(context.Background(), UpRequest{Session: "s2", Repo: testRepo(t, strings.ReplaceAll(file, "debug:", "nodebug:")), Scenario: "plain", DebugChecks: true}); err == nil {
		t.Fatal("a scenario without debug checks started a check debug run")
	}
}
