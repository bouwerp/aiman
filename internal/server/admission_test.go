package server

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/ptyruntime"
)

// promptWait starts session.prompt --wait on s1 and returns a channel with its
// answer.
func promptWait(ctx context.Context, t *testing.T, srv *Server, timeoutMS int) <-chan Response {
	t.Helper()
	out := make(chan Response, 1)
	go func() {
		out <- admCall(ctx, t, srv, "", scopeNoPID, "session.prompt",
			map[string]any{"id": "s1", "text": "go", "force": true, "wait": true, "timeout_ms": timeoutMS})
	}()
	return out
}

func TestPromptWaitDoesNotHoldSessionLock(t *testing.T) {
	srv, _ := unsettledServer(t, "s1")
	srv.SetHostLoad(config.HostLoadConfig{MaxInflight: 1})
	ctx, cancel := context.WithCancel(context.Background())
	answer := promptWait(ctx, t, srv, 60000)
	t.Cleanup(func() { cancel(); <-answer })
	waitFor(t, "the prompt's wait to pause", func() bool { return srv.RPCStats().Waiting == 1 })

	// A second prompt takes the only slot and needs the session lock; with the
	// lock held by the waiter neither could move.
	done := make(chan Response, 1)
	go func() {
		done <- admCall(context.Background(), t, srv, "", scopeNoPID, "session.prompt",
			map[string]any{"id": "s1", "text": "more", "force": true})
	}()
	select {
	case resp := <-done:
		if resp.Error != nil && resp.Error.Code != CodeServerBusy {
			t.Fatalf("second prompt: %+v", resp.Error)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a second prompt blocked behind the waiting prompt's session lock")
	}
	locked := make(chan struct{})
	go func() { srv.lockSession("s1")(); close(locked) }()
	select {
	case <-locked:
	case <-time.After(time.Second):
		t.Fatal("the session lock is held while the prompt waits")
	}
}

func TestPromptWaitIgnoresStateCachedBeforeSubmit(t *testing.T) {
	srv, _ := unsettledServer(t, "s1")
	srv.sessionStates.Store("s1", cachedState{State: string(domain.AgentStateIdle), Confidence: "high", At: time.Now().Add(-time.Second)})

	// session.wait reads the cache as it is.
	resp := admCall(context.Background(), t, srv, "", scopeNoPID, "session.wait", map[string]any{"id": "s1", "timeout_ms": 1000})
	if resp.Error != nil {
		t.Fatalf("session.wait on a cached idle: %+v", resp.Error)
	}

	// prompt --wait does not take the pre-prompt idle: the blank screen never
	// settles, so it times out.
	resp = <-promptWait(context.Background(), t, srv, 700)
	if errCode(resp) != CodeTimeout {
		t.Fatalf("prompt --wait over a stale idle = %+v, want %s", resp, CodeTimeout)
	}

	// A sample taken after the submit settles it. The fake prompt takes a few
	// seconds to type, so samples keep arriving until one is past the submit.
	answer := promptWait(context.Background(), t, srv, 15000)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case resp = <-answer:
			if resp.Error != nil {
				t.Fatalf("prompt --wait after a fresh sample: %+v", resp.Error)
			}
			return
		case <-tick.C:
			srv.sessionStates.Store("s1", cachedState{State: string(domain.AgentStateWaitingInput), Confidence: "high", At: time.Now()})
		}
	}
}

func TestBusyRefundsTheToken(t *testing.T) {
	a := newAdmission()
	a.configure(config.HostLoadConfig{PerCallerPerMinute: 60})
	now := time.Now()
	for range callerBurst {
		if _, ok := a.take("k", now); !ok {
			t.Fatal("burst ran out early")
		}
	}
	a.refund("k")
	if _, ok := a.take("k", now); !ok {
		t.Fatal("the refunded token was not there")
	}
	if _, ok := a.take("k", now); ok {
		t.Fatal("refund gave back more than one token")
	}
}

func TestBusyAnswerSpendsNoToken(t *testing.T) {
	srv, cp := unsettledServer(t, "s1")
	cp.gate = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	waited := make(chan struct{})
	t.Cleanup(func() { cancel(); close(cp.gate); <-waited })
	srv.sessionOfPID = func(pid int) (string, bool) { return "s1", pid == pidA }
	srv.SetHostLoad(config.HostLoadConfig{MaxInflight: 1})
	go func() {
		defer close(waited)
		admCall(ctx, t, srv, "", scopeNoPID, "session.wait", map[string]any{"id": "s1", "timeout_ms": 60000})
	}()
	waitFor(t, "the wait inside a capture", func() bool { return cp.active.Load() == 1 })
	for range callerBurst + 5 {
		if code := errCode(admCall(context.Background(), t, srv, "", pidA, "pty.list", nil)); code != CodeServerBusy {
			t.Fatalf("call = %q, want %s", code, CodeServerBusy)
		}
	}
	a := srv.admission()
	a.mu.Lock()
	tokens := a.buckets["s1"].tokens
	a.mu.Unlock()
	if tokens < callerBurst-1 {
		t.Fatalf("tokens = %.1f after only busy answers, want a full bucket", tokens)
	}
}

func TestIdleFullBucketsArePruned(t *testing.T) {
	a := newAdmission()
	now := time.Now()
	a.take("old", now)
	a.take("busy", now)
	for range callerBurst - 1 {
		a.take("busy", now)
	}
	later := now.Add(bucketIdle + time.Second)
	a.prunedAt = time.Time{}
	a.take("new", later)
	if _, ok := a.buckets["old"]; ok {
		t.Error("a full, idle bucket was kept")
	}
	if _, ok := a.buckets["new"]; !ok {
		t.Error("the bucket in use was dropped")
	}
}

// listCountingPTY counts List calls.
type listCountingPTY struct {
	*capturePTY
	lists atomic.Int32
}

func (l *listCountingPTY) List() []ptyruntime.SessionInfo {
	l.lists.Add(1)
	time.Sleep(20 * time.Millisecond)
	return l.capturePTY.List()
}

func TestStaleOwnerMapIsRebuiltOnce(t *testing.T) {
	srv, cp := unsettledServer(t, "s1")
	lp := &listCountingPTY{capturePTY: cp}
	srv.pty = lp
	srv.holderHeld = func(int) bool { return true }
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			srv.cachedHolderSessionOf(pidOutside)
		}()
	}
	wg.Wait()
	if n := lp.lists.Load(); n != 1 {
		t.Fatalf("owner map built %d times, want 1", n)
	}
}

func TestSessionReadPassesCaptureBudget(t *testing.T) {
	ids := []string{"s0", "s1", "s2", "s3", "s4", "s5", "s6", "s7"}
	srv, cp := unsettledServer(t, ids...)
	cp.delay = 50 * time.Millisecond
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			admCall(context.Background(), t, srv, "", scopeNoPID, "session.read", map[string]any{"id": id})
		}()
	}
	wg.Wait()
	if cp.calls.Load() != int32(len(ids)) {
		t.Fatalf("captures = %d, want %d", cp.calls.Load(), len(ids))
	}
	if peak := cp.peak.Load(); peak > DefaultMaxCaptures {
		t.Fatalf("%d session.read captures ran at once, want at most %d", peak, DefaultMaxCaptures)
	}
}
