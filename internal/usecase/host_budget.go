package usecase

import (
	"context"
	"sync"
	"time"
)

const (
	// HostBudgetSlots is the number of background slots, and separately of
	// foreground slots, one remote gets.
	HostBudgetSlots = 4
	// HostBudgetCooldown is how long a slot stays taken after a call that ended
	// in a local timeout: the remote process behind it may still be stalled.
	HostBudgetCooldown = 60 * time.Second

	// hostBudgetRecheck bounds how long a foreground waiter sleeps before it
	// looks at the cooldown clock again.
	hostBudgetRecheck = 250 * time.Millisecond
)

// HostBudget caps the laptop's calls to one remote so a host that answers
// nothing gets a bounded number of new ssh processes. Each remote has a pool of
// background slots (tick and event calls, never waited for) and a pool of
// foreground slots (calls a key started, waited for). Actions take no slot.
// A HostBudget is safe for concurrent use.
type HostBudget struct {
	now func() time.Time

	mu      sync.Mutex
	remotes map[string]*hostPools
}

type hostPools struct {
	background pool
	foreground pool
	// freed is closed and replaced whenever a foreground slot is released, to
	// wake waiters.
	freed chan struct{}
}

// pool counts slots in use and the times at which timed-out slots free up.
type pool struct {
	used     int
	cooldown []time.Time
}

// NewHostBudget returns an empty budget. now judges cooldowns; nil is time.Now.
func NewHostBudget(now func() time.Time) *HostBudget {
	if now == nil {
		now = time.Now
	}
	return &HostBudget{now: now, remotes: map[string]*hostPools{}}
}

func (b *HostBudget) pools(remote string) *hostPools {
	p, ok := b.remotes[remote]
	if !ok {
		p = &hostPools{freed: make(chan struct{})}
		b.remotes[remote] = p
	}
	return p
}

// take claims a slot when one is free. Caller holds b.mu.
func (p *pool) take(now time.Time) bool {
	live := p.cooldown[:0]
	for _, until := range p.cooldown {
		if now.Before(until) {
			live = append(live, until)
		}
	}
	p.cooldown = live
	if p.used+len(p.cooldown) >= HostBudgetSlots {
		return false
	}
	p.used++
	return true
}

// give returns a slot; a timed-out one is held until the cooldown ends.
func (p *pool) give(now time.Time, timedOut bool) {
	p.used--
	if timedOut {
		p.cooldown = append(p.cooldown, now.Add(HostBudgetCooldown))
	}
}

// TryAcquire takes a background slot without waiting. With none free it
// returns (nil, false). The release func is idempotent; release(true) keeps
// the slot taken for HostBudgetCooldown.
func (b *HostBudget) TryAcquire(remote string) (release func(timedOut bool), ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.pools(remote)
	if !p.background.take(b.now()) {
		return nil, false
	}
	return b.releaser(p, &p.background, false), true
}

// Acquire takes a foreground slot, waiting until one is free or ctx ends. On
// ctx's end it returns ctx's error and a nil release.
func (b *HostBudget) Acquire(ctx context.Context, remote string) (release func(timedOut bool), err error) {
	for {
		b.mu.Lock()
		p := b.pools(remote)
		if p.foreground.take(b.now()) {
			rel := b.releaser(p, &p.foreground, true)
			b.mu.Unlock()
			return rel, nil
		}
		freed := p.freed
		b.mu.Unlock()

		timer := time.NewTimer(hostBudgetRecheck)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-freed:
		case <-timer.C:
		}
		timer.Stop()
	}
}

func (b *HostBudget) releaser(p *hostPools, slots *pool, wake bool) func(bool) {
	var once sync.Once
	return func(timedOut bool) {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			slots.give(b.now(), timedOut)
			if wake && !timedOut {
				close(p.freed)
				p.freed = make(chan struct{})
			}
		})
	}
}
