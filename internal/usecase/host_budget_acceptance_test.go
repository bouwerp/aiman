//go:build pileup_a

package usecase

// Contract (docs/specs/client-pileup-guard.md, Behaviour P2):
//
//   - const HostBudgetSlots = 4
//     Background slots per remote, and separately foreground slots per remote.
//
//   - const HostBudgetCooldown = 60 * time.Second
//     How long a slot stays taken after a call that ended in a local timeout.
//
//   - func NewHostBudget(now func() time.Time) *HostBudget
//     now is the clock the cooldown is judged by; nil is time.Now.
//
//   - func (b *HostBudget) TryAcquire(remote string) (release func(timedOut bool), ok bool)
//     Takes a background slot (tick and event calls) without waiting. With no
//     slot free it returns (nil, false). release(false) frees the slot at once;
//     release(true) keeps it taken until HostBudgetCooldown has passed on now.
//
//   - func (b *HostBudget) Acquire(ctx context.Context, remote string) (release func(timedOut bool), err error)
//     Takes a foreground slot (calls a key started), waiting until one is free
//     or ctx ends; on ctx's end it returns ctx's error and a nil release. The
//     foreground and background pools of one remote are independent, and each
//     remote has its own pools.
//
// Actions (a key that changes something) take no slot; the TUI side of that
// rule is pinned in internal/ui/poll_budget_acceptance_test.go.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type hbClock struct {
	mu sync.Mutex
	t  time.Time
}

func newHBClock() *hbClock {
	return &hbClock{t: time.Date(2026, 11, 13, 11, 30, 0, 0, time.UTC)}
}

func (c *hbClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *hbClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// hbBlockingExec is a remote whose every command blocks until open is closed,
// like a host paging every exec back in.
type hbBlockingExec struct {
	mu      sync.Mutex
	started int
	open    chan struct{}
}

func newHBBlockingExec() *hbBlockingExec {
	return &hbBlockingExec{open: make(chan struct{})}
}

func (e *hbBlockingExec) Execute(ctx context.Context, _ string) (string, error) {
	e.mu.Lock()
	e.started++
	e.mu.Unlock()
	select {
	case <-e.open:
		return `{"result":{}}`, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (e *hbBlockingExec) WriteFile(context.Context, string, []byte) error { return nil }

func (e *hbBlockingExec) Started() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.started
}

func hbWaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// hbFillBackground takes every background slot of remote and returns the
// release funcs.
func hbFillBackground(t *testing.T, b *HostBudget, remote string) []func(bool) {
	t.Helper()
	var rels []func(bool)
	for i := 0; i < HostBudgetSlots; i++ {
		rel, ok := b.TryAcquire(remote)
		if !ok || rel == nil {
			t.Fatalf("background slot %d of %s: not granted", i+1, remote)
		}
		rels = append(rels, rel)
	}
	return rels
}

func hbFillForeground(t *testing.T, b *HostBudget, remote string) []func(bool) {
	t.Helper()
	var rels []func(bool)
	for i := 0; i < HostBudgetSlots; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		rel, err := b.Acquire(ctx, remote)
		cancel()
		if err != nil || rel == nil {
			t.Fatalf("foreground slot %d of %s: %v", i+1, remote, err)
		}
		rels = append(rels, rel)
	}
	return rels
}

func TestAcceptanceHostBudgetSlotCount(t *testing.T) {
	if HostBudgetSlots != 4 {
		t.Fatalf("HostBudgetSlots = %d, want 4", HostBudgetSlots)
	}
	if HostBudgetCooldown != 60*time.Second {
		t.Fatalf("HostBudgetCooldown = %s, want 60s", HostBudgetCooldown)
	}
}

// Four background calls stuck in a blocking executor hold every slot: the
// fifth is refused at once with no release to call, and gets a slot as soon
// as one of the four returns.
func TestAcceptanceHostBudgetSkipsFifthBackgroundCall(t *testing.T) {
	clock := newHBClock()
	b := NewHostBudget(clock.Now)
	exec := newHBBlockingExec()

	var wg sync.WaitGroup
	for i := 0; i < HostBudgetSlots; i++ {
		rel, ok := b.TryAcquire("regent0")
		if !ok || rel == nil {
			t.Fatalf("background call %d: no slot", i+1)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := ServeRPC(ctx, exec, "todo.summary", map[string]any{})
			rel(err != nil && errors.Is(err, context.DeadlineExceeded))
		}()
	}
	hbWaitFor(t, "four calls in the executor", func() bool { return exec.Started() == HostBudgetSlots })

	rel, ok := b.TryAcquire("regent0")
	if ok || rel != nil {
		t.Fatal("a fifth background call got a slot while four are in flight")
	}

	close(exec.open)
	wg.Wait()
	rel, ok = b.TryAcquire("regent0")
	if !ok || rel == nil {
		t.Fatal("no slot after the four calls returned")
	}
	rel(false)
}

// A fifth foreground call waits for a slot instead of being skipped, and the
// background pool being full does not hold it up.
func TestAcceptanceHostBudgetForegroundWaits(t *testing.T) {
	b := NewHostBudget(newHBClock().Now)
	hbFillBackground(t, b, "regent0")
	fg := hbFillForeground(t, b, "regent0")

	got := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rel, err := b.Acquire(ctx, "regent0")
		if err == nil {
			if rel == nil {
				err = errors.New("nil release with no error")
			} else {
				rel(false)
			}
		}
		got <- err
	}()

	select {
	case err := <-got:
		t.Fatalf("the fifth foreground call did not wait (err %v)", err)
	case <-time.After(50 * time.Millisecond):
	}

	fg[0](false)
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("the waiting foreground call failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiting foreground call never got the freed slot")
	}

	// The waiter released its slot, so one is free again: take it, then a wait
	// whose context ends gives up with its error and nothing to release.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	rel, err := b.Acquire(ctx, "regent0")
	cancel()
	if err != nil || rel == nil {
		t.Fatalf("the freed foreground slot was not granted: %v", err)
	}
	defer rel(false)
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	rel2, err := b.Acquire(ctx, "regent0")
	if err == nil || rel2 != nil {
		t.Fatal("a wait on a full foreground pool whose context ended got a slot")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a wait whose context ended returned %v, want the context's error", err)
	}
}

// A full remote leaves another remote's pools untouched.
func TestAcceptanceHostBudgetOtherRemoteUnaffected(t *testing.T) {
	b := NewHostBudget(newHBClock().Now)
	hbFillBackground(t, b, "regent0")
	hbFillForeground(t, b, "regent0")

	rel, ok := b.TryAcquire("devbox")
	if !ok || rel == nil {
		t.Fatal("devbox background was refused because regent0 is full")
	}
	rel(false)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	frel, err := b.Acquire(ctx, "devbox")
	if err != nil || frel == nil {
		t.Fatalf("devbox foreground waited on regent0: %v", err)
	}
	frel(false)
}

// A call that ended in a local timeout keeps its slot for the cooldown,
// because its remote process may still be stalled; an ordinary end frees it
// at once.
func TestAcceptanceHostBudgetTimeoutKeepsSlotForCooldown(t *testing.T) {
	clock := newHBClock()
	b := NewHostBudget(clock.Now)
	rels := hbFillBackground(t, b, "regent0")

	rels[0](false)
	rel, ok := b.TryAcquire("regent0")
	if !ok {
		t.Fatal("an ordinary end did not free its slot")
	}

	rel(true)
	if _, ok := b.TryAcquire("regent0"); ok {
		t.Fatal("a timed-out call's slot was free at once")
	}
	clock.Add(HostBudgetCooldown - time.Second)
	if _, ok := b.TryAcquire("regent0"); ok {
		t.Fatal("a timed-out call's slot was free before the cooldown ended")
	}
	clock.Add(2 * time.Second)
	rel, ok = b.TryAcquire("regent0")
	if !ok || rel == nil {
		t.Fatal("the slot stayed taken after the cooldown")
	}
	rel(false)

	// The foreground pool keeps a timed-out slot the same way.
	fg := hbFillForeground(t, b, "regent0")
	fg[0](true)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	_, err := b.Acquire(ctx, "regent0")
	cancel()
	if err == nil {
		t.Fatal("a timed-out foreground slot was free at once")
	}
	clock.Add(HostBudgetCooldown + time.Second)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	frel, err := b.Acquire(ctx, "regent0")
	cancel()
	if err != nil || frel == nil {
		t.Fatalf("the foreground slot stayed taken after the cooldown: %v", err)
	}
	frel(false)
}
