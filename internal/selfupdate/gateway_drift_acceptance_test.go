package selfupdate_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/selfupdate"
)

// Gateway drift (docs/designs gateway-update rev 2): serve's manager also
// brings an installed aiman-gateway that is older than serve up to serve's tag,
// with a gateway-only script that never touches serve.

func gatewayScript(tag string) string { return "#!/bin/sh\n# gateway " + tag + "\n" }

type fakeGateway struct {
	mu    sync.Mutex
	gw    selfupdate.Gateway
	err   error
	calls int
	ctxs  []context.Context
}

func (f *fakeGateway) read(ctx context.Context) (selfupdate.Gateway, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.ctxs = append(f.ctxs, ctx)
	return f.gw, f.err
}

func (f *fakeGateway) set(gw selfupdate.Gateway, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gw, f.err = gw, err
}

type gwHarness struct {
	*harness
	gw      *fakeGateway
	clock   time.Time
	clockMu sync.Mutex
	behind  []string
}

func (h *gwHarness) advance(d time.Duration) {
	h.clockMu.Lock()
	defer h.clockMu.Unlock()
	h.clock = h.clock.Add(d)
}

func (h *gwHarness) behindNotes() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.behind...)
}

func newGwHarness(t *testing.T, serve string, mode selfupdate.Mode, gw selfupdate.Gateway) *gwHarness {
	t.Helper()
	h := &gwHarness{harness: newHarness(t, serve, mode), gw: &fakeGateway{gw: gw}, clock: testNow}
	h.m.Now = func() time.Time {
		h.clockMu.Lock()
		defer h.clockMu.Unlock()
		return h.clock
	}
	h.m.ReadGateway = h.gw.read
	h.m.GatewayScript = gatewayScript
	h.m.OnGatewayBehind = func(from, to string) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.behind = append(h.behind, from+"@"+to)
	}
	h.fetcher.set(ready(serve), true)
	return h
}

func running(version string) selfupdate.Gateway {
	return selfupdate.Gateway{Installed: true, Version: version, Running: true}
}

func (h *gwHarness) scripts() []string {
	h.runner.mu.Lock()
	defer h.runner.mu.Unlock()
	return append([]string(nil), h.runner.scripts...)
}

func TestGatewayBehindCurrentServeRunsGatewayScriptOnly(t *testing.T) {
	h := newGwHarness(t, "v0.42.0", selfupdate.ModeAuto, running("v0.41.0"))
	st := pollWithin(t, h.m)

	scripts := h.scripts()
	if len(scripts) != 1 || scripts[0] != gatewayScript("v0.42.0") {
		t.Fatalf("scripts = %q, want only the gateway script at serve's tag", scripts)
	}
	if got := h.runner.started(); len(got) != 1 || got[0] != "v0.42.0" {
		t.Fatalf("started tags = %v, want [v0.42.0]", got)
	}
	a := h.state(t).Attempt
	if a == nil || a.Kind != selfupdate.AttemptKindGateway || a.From != "v0.41.0" || a.To != "v0.42.0" || a.Status != selfupdate.AttemptStarted {
		t.Fatalf("attempt = %+v, want started gateway attempt v0.41.0 -> v0.42.0", a)
	}
	if st.Status != selfupdate.StatusCurrent {
		t.Fatalf("host status = %q during a gateway repair, want %q (it describes serve)", st.Status, selfupdate.StatusCurrent)
	}
	if st.Gateway == nil || st.Gateway.Status != selfupdate.GatewayUpdating {
		t.Fatalf("gateway status = %+v, want %q", st.Gateway, selfupdate.GatewayUpdating)
	}
}

func TestGatewayNotOlderIsLeftAlone(t *testing.T) {
	cases := map[string]struct {
		gw   selfupdate.Gateway
		err  error
		want string
	}{
		"same":          {running("v0.42.0"), nil, selfupdate.GatewayCurrent},
		"newer":         {running("v0.43.0"), nil, selfupdate.GatewayCurrent},
		"stopped same":  {selfupdate.Gateway{Installed: true, Version: "v0.42.0"}, nil, selfupdate.GatewayCurrent},
		"not installed": {selfupdate.Gateway{}, nil, selfupdate.GatewayNotInstalled},
		"unparseable":   {running("aiman-gateway dev"), nil, selfupdate.GatewayUnknown},
		"read error":    {selfupdate.Gateway{}, errors.New("timeout"), selfupdate.GatewayUnknown},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := newGwHarness(t, "v0.42.0", selfupdate.ModeAuto, c.gw)
			h.gw.set(c.gw, c.err)
			st := pollWithin(t, h.m)
			if s := h.scripts(); len(s) != 0 {
				t.Fatalf("scripts = %q, want none", s)
			}
			if st.Gateway == nil || st.Gateway.Status != c.want {
				t.Fatalf("gateway status = %+v, want %q", st.Gateway, c.want)
			}
		})
	}
}

func TestGatewayReadHasDeadlineAndRunsOutsideTheLock(t *testing.T) {
	h := newGwHarness(t, "v0.42.0", selfupdate.ModeAuto, running("v0.42.0"))
	// While the read runs, Status must still answer: the read holds no lock.
	h.m.ReadGateway = func(ctx context.Context) (selfupdate.Gateway, error) {
		dl, ok := ctx.Deadline()
		if !ok || time.Until(dl) > 6*time.Second {
			t.Errorf("gateway read deadline = %v (set %v), want at most about 5s", time.Until(dl), ok)
		}
		done := make(chan struct{})
		go func() { _ = h.m.Status(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Status blocked while the gateway read ran")
		}
		return running("v0.42.0"), nil
	}
	pollWithin(t, h.m)
}

func TestNewerServeReleaseKeepsTheServeScript(t *testing.T) {
	h := newGwHarness(t, "v0.42.0", selfupdate.ModeAuto, running("v0.41.0"))
	h.fetcher.set(ready("v0.43.0"), true)
	pollWithin(t, h.m)
	scripts := h.scripts()
	if len(scripts) != 1 || scripts[0] != testScript("v0.43.0") {
		t.Fatalf("scripts = %q, want only the serve script for v0.43.0 (its lockstep carries the gateway)", scripts)
	}
	if a := h.state(t).Attempt; a == nil || a.Kind == selfupdate.AttemptKindGateway {
		t.Fatalf("attempt = %+v, want a serve attempt", a)
	}
}

func TestGatewayRepairSettlesAsGatewayAttempt(t *testing.T) {
	for _, exit := range []int{0, 3} {
		h := newGwHarness(t, "v0.42.0", selfupdate.ModeAuto, running("v0.41.0"))
		pollWithin(t, h.m)
		a := h.state(t).Attempt
		writeResultFile(t, h.m.ResultPath, "v0.42.0", exit, a.At.Add(time.Second))
		if exit == 0 {
			h.gw.set(running("v0.42.0"), nil)
		}
		h.advance(10 * time.Second)
		st := pollWithin(t, h.m)

		applied := h.appliedAttempts()
		if len(applied) != 1 || applied[0].Kind != selfupdate.AttemptKindGateway {
			t.Fatalf("exit %d: applied = %+v, want one gateway attempt", exit, applied)
		}
		if exit == 0 {
			if applied[0].Status != selfupdate.AttemptOK || st.Gateway.Status != selfupdate.GatewayCurrent {
				t.Fatalf("exit 0: attempt %+v, gateway %+v, want ok and current", applied[0], st.Gateway)
			}
			continue
		}
		if applied[0].Status != selfupdate.AttemptFailed || !strings.Contains(applied[0].Error, "gateway") {
			t.Fatalf("exit %d: attempt %+v, want failed with a gateway error", exit, applied[0])
		}
		if st.Gateway.Status != selfupdate.GatewayFailed || st.Status != selfupdate.StatusCurrent {
			t.Fatalf("exit %d: host %q gateway %+v, want host current, gateway failed", exit, st.Status, st.Gateway)
		}
	}
}

func TestFailedGatewayRepairRetriesWithGatewayScript(t *testing.T) {
	h := newGwHarness(t, "v0.42.0", selfupdate.ModeAuto, running("v0.41.0"))
	h.seed(t, selfupdate.State{Latest: ready("v0.42.0"), Attempt: &selfupdate.Attempt{
		At: testNow, From: "v0.41.0", To: "v0.42.0", Kind: selfupdate.AttemptKindGateway,
		Status: selfupdate.AttemptFailed, Exit: 3, Error: "gateway install failed (exit 3)",
	}})
	h.fetcher.set(selfupdate.Release{}, false)

	pollWithin(t, h.m)
	if s := h.scripts(); len(s) != 0 {
		t.Fatalf("scripts inside the retry interval = %q, want none", s)
	}
	h.advance(selfupdate.DefaultRetryAfter + time.Minute)
	pollWithin(t, h.m)
	if s := h.scripts(); len(s) != 1 || s[0] != gatewayScript("v0.42.0") {
		t.Fatalf("scripts after the retry interval = %q, want the gateway script, never serve's", s)
	}
}

func TestFailedLockstepWithServeAtTagTakesGatewayPath(t *testing.T) {
	// serve updated to v0.42.0 but its gateway lockstep failed: serve is at
	// the tag, the gateway is not.
	h := newGwHarness(t, "v0.42.0", selfupdate.ModeAuto, running("v0.41.0"))
	h.seed(t, selfupdate.State{Latest: ready("v0.42.0"), Attempt: &selfupdate.Attempt{
		At: testNow, From: "v0.41.0", To: "v0.42.0", Kind: selfupdate.AttemptKindServe,
		Status: selfupdate.AttemptFailed, Exit: 4, Error: "gateway lockstep failed (exit 4)",
	}})
	h.fetcher.set(selfupdate.Release{}, false)

	// host.update inside the retry interval repairs the gateway at once.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out := h.m.Request(ctx)
	if !out.Accepted || out.Target != "v0.42.0" || out.Reason != selfupdate.ReasonGateway {
		t.Fatalf("Request = %+v, want accepted gateway repair to v0.42.0", out)
	}
	if s := h.scripts(); len(s) != 1 || s[0] != gatewayScript("v0.42.0") {
		t.Fatalf("scripts = %q, want the gateway script, never a serve reinstall", s)
	}
}

func TestRequestRepairsGatewayAndReportsARunningRepair(t *testing.T) {
	h := newGwHarness(t, "v0.42.0", selfupdate.ModeNotify, running("v0.41.0"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out := h.m.Request(ctx)
	if !out.Accepted || out.Reason != selfupdate.ReasonGateway || out.Target != "v0.42.0" {
		t.Fatalf("Request = %+v, want accepted gateway repair", out)
	}
	setAlive(t, func(int) bool { return true })
	out = h.m.Request(ctx)
	if out.Accepted || !strings.Contains(out.Reason, "gateway") || !strings.Contains(out.Reason, "running") {
		t.Fatalf("second Request = %+v, want refused: the gateway update is already running", out)
	}
	if s := h.scripts(); len(s) != 1 {
		t.Fatalf("scripts = %q, want one", s)
	}
}

func TestNotifyModeNotifiesGatewayDriftOnce(t *testing.T) {
	h := newGwHarness(t, "v0.42.0", selfupdate.ModeNotify, running("v0.41.0"))
	pollWithin(t, h.m)
	pollWithin(t, h.m)
	if got := h.behindNotes(); len(got) != 1 || got[0] != "v0.41.0@v0.42.0" {
		t.Fatalf("gateway notes = %v, want one v0.41.0@v0.42.0", got)
	}
	if s := h.scripts(); len(s) != 0 {
		t.Fatalf("notify mode started %q", s)
	}
	if n := h.state(t).NotifiedGateway; n != "v0.41.0@v0.42.0" {
		t.Fatalf("NotifiedGateway = %q", n)
	}
	if n := h.state(t).Notified; n != "" {
		t.Fatalf("Notified = %q, the serve key must not be reused", n)
	}
}

func TestModeOffAndBusyGateDoNothing(t *testing.T) {
	h := newGwHarness(t, "v0.42.0", selfupdate.ModeOff, running("v0.41.0"))
	pollWithin(t, h.m)
	if s := h.scripts(); len(s) != 0 {
		t.Fatalf("mode off started %q", s)
	}
	h = newGwHarness(t, "v0.42.0", selfupdate.ModeAuto, running("v0.41.0"))
	h.m.Busy = []func() int{func() int { return 1 }}
	pollWithin(t, h.m)
	if s := h.scripts(); len(s) != 0 {
		t.Fatalf("busy host started %q", s)
	}
}

func TestStatusReadsTheGatewayLive(t *testing.T) {
	h := newGwHarness(t, "v0.42.0", selfupdate.ModeNotify, running("v0.41.0"))
	if st := h.m.Status(); st.Gateway == nil || st.Gateway.Status != selfupdate.GatewayBehind || st.Gateway.Version != "v0.41.0" {
		t.Fatalf("Status gateway = %+v, want behind v0.41.0 without a poll", st.Gateway)
	}
	h.gw.set(running("v0.42.0"), nil)
	h.advance(6 * time.Second) // past the 5s cache
	if st := h.m.Status(); st.Gateway.Status != selfupdate.GatewayCurrent || st.Gateway.Version != "v0.42.0" {
		t.Fatalf("Status gateway = %+v, want current v0.42.0 once the cache expires", st.Gateway)
	}
}

func TestNoGatewayReaderMeansNoGatewayBlock(t *testing.T) {
	h := newHarness(t, "v0.42.0", selfupdate.ModeAuto)
	h.fetcher.set(ready("v0.42.0"), true)
	if st := pollWithin(t, h.m); st.Gateway != nil {
		t.Fatalf("Gateway = %+v, want nil when ReadGateway is unset", st.Gateway)
	}
}
