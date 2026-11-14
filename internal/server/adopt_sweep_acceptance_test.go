//go:build pileup_b

package server

// Contract:
//
//   - func (s *Server) adoptSweep(ctx context.Context)
//     One pass of the auto-adopt sweep (spec P0). For each running holder
//     whose cgroup is not under the sessions slice it runs the adopt path
//     shared with session.adopt_scope, for that holder's scope key, with
//     resources.session merged with the session's override, and records
//     resources.adopted with actor domain.EventActorAiman. It does nothing
//     when ResourceOptions.Systemd is nil. When serve's own cgroup
//     (cgroupOf(os.Getpid())) is not under Systemd.Root it never adopts and
//     logs one line for the life of the Server. A failed adoption is tried
//     again on the next pass; its error is logged once per session and again
//     only when the error text changes. The timer loop that calls it (30s
//     after start, then every 5 minutes) is not part of this contract.
//   - Server field cgroupOf func(pid int) string
//     The cgroup directory of pid; nil means cgroup.Of. The sweep, the
//     serve-in-user-manager check and resources.get's unscoped list read
//     cgroups through it.
//   - Server field adoptProcs func(ctx context.Context, key string, l cgroup.Limits, pids func() []int) (int, error)
//     Moves the processes pids returns into key's scope; nil means
//     resourceOptions().Systemd.Adopt. Both the sweep and
//     session.adopt_scope call it.
//   - type UnscopedSession struct { ID string `json:"id"`; Name string `json:"name,omitempty"` }
//   - ResourcesReport field Unscoped []UnscopedSession `json:"unscoped"`
//     resources.get computes it at call time: every running holder whose
//     cgroup is not under the sessions slice, including while adoption is
//     impossible (serve outside the user manager).
//   - cgroup.Snapshot field ServeUnit *cgroup.Usage `json:"serve_unit,omitempty"`
//     (serve's own cgroup, read like the slices; asserted by the cmd test).

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bouwerp/aiman/internal/cgroup"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/ptyruntime/ptyfake"
)

// syncBuffer collects log output from any goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, l := range strings.Split(b.buf.String(), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func (b *syncBuffer) count(sub string) int {
	n := 0
	for _, l := range b.lines() {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) })
	return buf
}

// adoptRig is resourcesRig with session "b" running in a real child process
// (scopeProcs walks /proc) whose cgroup the test controls. Session "b" has
// no scope in the rig's fake cgroup tree.
type adoptRig struct {
	srv   *Server
	sd    *cgroup.Systemd
	pid   int
	mu    sync.Mutex
	own   string // serve's own cgroup
	where string // the cgroup of every other pid
	calls []adoptCall
	fail  error
}

type adoptCall struct {
	key    string
	limits cgroup.Limits
	pids   []int
}

func newAdoptRig(t *testing.T) *adoptRig {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("scopeProcs needs /proc")
	}
	child := exec.Command("tail", "-f", "/dev/null")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })

	srv, _ := resourcesRig(t)
	opts := srv.resourceOptions()
	if _, err := opts.Overrides.Set("b", cgroup.Limits{MemoryHigh: 12 << 30}); err != nil {
		t.Fatal(err)
	}
	srv.pty.(*ptyfake.FakePTY).Add(ptyruntime.SessionInfo{ID: "b", Name: "b", Status: ptyruntime.StatusRunning, PID: child.Process.Pid})
	srv.holderHeld = func(int) bool { return true }

	sd := opts.Systemd
	serveCg := filepath.Join(sd.Root, "app.slice", "aiman-serve.service")
	r := &adoptRig{srv: srv, sd: sd, pid: child.Process.Pid, own: serveCg, where: serveCg}
	srv.cgroupOf = func(pid int) string {
		r.mu.Lock()
		defer r.mu.Unlock()
		if pid == os.Getpid() {
			return r.own
		}
		return r.where
	}
	srv.adoptProcs = func(_ context.Context, key string, l cgroup.Limits, pids func() []int) (int, error) {
		listed := pids()
		r.mu.Lock()
		defer r.mu.Unlock()
		r.calls = append(r.calls, adoptCall{key: key, limits: l, pids: listed})
		if r.fail != nil {
			return 0, r.fail
		}
		r.where = sd.ScopeDir(key)
		return len(listed), nil
	}
	return r
}

func (r *adoptRig) adoptCalls() []adoptCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

func unscopedIDs(rep ResourcesReport) []string {
	var ids []string
	for _, u := range rep.Unscoped {
		ids = append(ids, u.ID)
	}
	return ids
}

func adoptedEvents(t *testing.T, srv *Server, id string) []domain.SessionEvent {
	t.Helper()
	lister, ok := srv.repo.(interface {
		ListSessionEvents(ctx context.Context, sessionID string, sinceSeq int64, limit int) ([]domain.SessionEvent, error)
	})
	if !ok {
		t.Fatal("test repo cannot list session events")
	}
	events, err := lister.ListSessionEvents(context.Background(), id, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []domain.SessionEvent
	for _, ev := range events {
		if ev.Kind == domain.EventResourcesAdopted {
			out = append(out, ev)
		}
	}
	return out
}

// A holder outside the sessions slice is adopted once, with its session's
// limits and its own processes, and the ledger says aiman did it.
// resources.get lists it under unscoped before and not after.
func TestAcceptanceAdoptSweepAdoptsAnUnscopedHolderOnce(t *testing.T) {
	r := newAdoptRig(t)
	ctx := context.Background()

	before := resourcesOf(t, rulesCall(t, r.srv, "", "resources.get", nil))
	if !slices.Contains(unscopedIDs(before), "b") {
		t.Fatalf("before the sweep, unscoped = %+v; want b listed", before.Unscoped)
	}
	for _, u := range before.Unscoped {
		if u.ID == "b" && u.Name != "b" {
			t.Fatalf("unscoped row %+v; want name b", u)
		}
	}

	r.srv.adoptSweep(ctx)
	r.srv.adoptSweep(ctx)

	calls := r.adoptCalls()
	if len(calls) != 1 {
		t.Fatalf("adopt called %d times over two passes; want once: %+v", len(calls), calls)
	}
	c := calls[0]
	if c.key != "b" {
		t.Fatalf("adopted key %q; want b", c.key)
	}
	if want := r.srv.resourceOptions().sessionLimits("b"); c.limits != want || c.limits.MemoryHigh != 12<<30 {
		t.Fatalf("adopt limits %+v; want the defaults merged with the override %+v", c.limits, want)
	}
	if !slices.Contains(c.pids, r.pid) {
		t.Fatalf("adopt listed pids %v; want the session's process %d", c.pids, r.pid)
	}

	events := adoptedEvents(t, r.srv, "b")
	if len(events) != 1 {
		t.Fatalf("resources.adopted rows: %+v; want one", events)
	}
	if events[0].Actor != domain.EventActorAiman {
		t.Fatalf("resources.adopted actor %q; want %q", events[0].Actor, domain.EventActorAiman)
	}

	after := resourcesOf(t, rulesCall(t, r.srv, "", "resources.get", nil))
	if slices.Contains(unscopedIDs(after), "b") {
		t.Fatalf("after the sweep, unscoped = %+v; want b gone", after.Unscoped)
	}
}

// Under the nohup supervisor serve is outside the user manager, the kernel
// refuses the move, so the sweep never asks and says so once.
func TestAcceptanceAdoptSweepOutsideUserManagerNeverAdopts(t *testing.T) {
	r := newAdoptRig(t)
	outside := filepath.Join(r.sd.UserSliceDir(), "session-3.scope")
	if strings.HasPrefix(outside, r.sd.Root+"/") {
		t.Fatalf("fixture %s is inside the manager root %s", outside, r.sd.Root)
	}
	r.mu.Lock()
	r.own, r.where = outside, outside
	r.mu.Unlock()
	logs := captureLog(t)
	ctx := context.Background()

	r.srv.adoptSweep(ctx)
	r.srv.adoptSweep(ctx)

	if calls := r.adoptCalls(); len(calls) != 0 {
		t.Fatalf("adopt called from a serve outside the user manager: %+v", calls)
	}
	if lines := logs.lines(); len(lines) != 1 {
		t.Fatalf("log lines over two passes: %q; want exactly one", lines)
	}
	if events := adoptedEvents(t, r.srv, "b"); len(events) != 0 {
		t.Fatalf("resources.adopted recorded with nothing moved: %+v", events)
	}
	rep := resourcesOf(t, rulesCall(t, r.srv, "", "resources.get", nil))
	if !slices.Contains(unscopedIDs(rep), "b") {
		t.Fatalf("unscoped = %+v; want b listed while adoption is impossible", rep.Unscoped)
	}
}

// A failed adoption is tried again on the next pass; the failure is logged
// once, and again only when its text changes.
func TestAcceptanceAdoptSweepRetriesAFailureAndLogsOnce(t *testing.T) {
	r := newAdoptRig(t)
	r.mu.Lock()
	r.fail = errors.New("cgroup: starting aiman-session-b.scope: boom-1")
	r.mu.Unlock()
	logs := captureLog(t)
	ctx := context.Background()

	r.srv.adoptSweep(ctx)
	r.srv.adoptSweep(ctx)
	if n := len(r.adoptCalls()); n != 2 {
		t.Fatalf("adopt called %d times over two failing passes; want 2", n)
	}
	if n := logs.count("boom-1"); n != 1 {
		t.Fatalf("failure logged %d times: %q; want once", n, logs.lines())
	}

	r.mu.Lock()
	r.fail = errors.New("cgroup: starting aiman-session-b.scope: boom-2")
	r.mu.Unlock()
	r.srv.adoptSweep(ctx)
	if n := len(r.adoptCalls()); n != 3 {
		t.Fatalf("adopt called %d times after three passes; want 3", n)
	}
	if n := logs.count("boom-2"); n != 1 {
		t.Fatalf("changed failure logged %d times: %q; want once", n, logs.lines())
	}
	if events := adoptedEvents(t, r.srv, "b"); len(events) != 0 {
		t.Fatalf("resources.adopted recorded for a failed adoption: %+v", events)
	}

	// Once the host recovers, the next pass adopts it.
	r.mu.Lock()
	r.fail = nil
	r.mu.Unlock()
	r.srv.adoptSweep(ctx)
	if events := adoptedEvents(t, r.srv, "b"); len(events) != 1 {
		t.Fatalf("resources.adopted rows after recovery: %+v; want one", events)
	}
}
