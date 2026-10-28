package agentupdate

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

// Status values reported per agent. A deferred agent reports the gate reason
// itself, such as "deferred: 2 sessions busy".
const (
	StatusCurrent       = "current"
	StatusAvailable     = "update available"
	StatusLatestUnknown = "latest unknown"
	StatusNeedsRoot     = "needs root"
	StatusUpdating      = "updating"
	StatusFailed        = "failed"
	StatusNotInstalled  = "not installed"
)

// Loop timings used when the Manager fields are zero.
const (
	DefaultFirstProbeDelay = 2 * time.Minute
	DefaultCheckEvery      = 6 * time.Hour
	// DefaultApplyEvery is how often auto mode retries outdated agents from
	// the stored state between probes, so a window shorter than check_every
	// is not missed and deferred updates retry.
	DefaultApplyEvery = 15 * time.Minute
)

// AgentStatus is one row of agents.status: the stored state plus what serve
// would do about it now.
type AgentStatus struct {
	AgentState
	Status string `json:"status"`
	// Reason is the gate reason or the last update error, when there is one.
	Reason string `json:"reason,omitempty"`
	// Held is true for agents on agents.update.hold.
	Held bool `json:"held,omitempty"`
}

// Outcome answers one agents.update target.
type Outcome struct {
	Agent    string `json:"agent"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
}

// Applied describes one finished update attempt.
type Applied struct {
	Agent string
	// From is the version before the update and To the version the
	// re-probe found; To is empty when the re-probe failed.
	From string
	To   string
	OK   bool
	// Err is the failure, such as "needs root: <command>".
	Err string
	// Auto is true for updates auto mode started inside the window.
	Auto bool
}

// Manager owns the version state on one host: it probes, reports status,
// and applies updates through the Runner, one at a time.
type Manager struct {
	Prober    *Prober
	Runner    *Runner
	Entries   []Entry
	StatePath string
	Policy    Policy

	CheckEvery      time.Duration
	FirstProbeDelay time.Duration
	ApplyEvery      time.Duration

	// Sessions lists the live sessions the gate checks.
	Sessions func(ctx context.Context) ([]LiveSession, error)
	// AfterUpdate runs after a successful update and its re-probe. Serve
	// reinstalls agent hooks here.
	AfterUpdate func(agent string)
	// OnNewVersion runs once per (agent, latest) when a probe first sees a
	// newer version and the policy notifies. The version is recorded as
	// notified only when this is set.
	OnNewVersion func(AgentState)
	// OnApplied runs after every update attempt that reached the update
	// command, successful or not.
	OnApplied func(Applied)
	// BaseCtx parents updates started by Request, which outlive the
	// request. Nil is context.Background.
	BaseCtx context.Context
	Logf    func(format string, args ...any)
	Now     func() time.Time

	mu       sync.Mutex
	state    *State
	updating map[string]bool
	// applyMu holds the gate check and the update together, so a session
	// cannot start working between them unnoticed by another update.
	applyMu sync.Mutex
	wg      sync.WaitGroup
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Manager) logf(format string, args ...any) {
	if m.Logf != nil {
		m.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

func (m *Manager) entries() []Entry {
	if m.Entries != nil {
		return m.Entries
	}
	return Catalog()
}

func (m *Manager) entry(agent string) (Entry, bool) {
	for _, e := range m.entries() {
		if e.Agent == agent {
			return e, true
		}
	}
	return Entry{}, false
}

// loadLocked returns the state, reading StatePath on first use. Callers
// hold m.mu.
func (m *Manager) loadLocked() *State {
	if m.state != nil {
		return m.state
	}
	st, err := LoadState(m.StatePath)
	if err != nil {
		m.logf("agent updates: state %s: %v", m.StatePath, err)
		st = NewState()
	}
	m.state = st
	if m.updating == nil {
		m.updating = map[string]bool{}
	}
	return st
}

func (m *Manager) saveLocked() {
	if m.StatePath == "" {
		return
	}
	if err := m.state.Save(m.StatePath); err != nil {
		m.logf("agent updates: save state: %v", err)
	}
}

func (m *Manager) live(ctx context.Context) []LiveSession {
	if m.Sessions == nil {
		return nil
	}
	list, err := m.Sessions(ctx)
	if err != nil {
		m.logf("agent updates: sessions: %v", err)
	}
	return list
}

// probeAll probes every entry concurrently, so an on-demand refresh stays
// within one socket call's deadline.
func (m *Manager) probeAll(ctx context.Context) map[string]AgentState {
	entries := m.entries()
	type result struct {
		st AgentState
		ok bool
	}
	results := make([]result, len(entries))
	var wg sync.WaitGroup
	for i, e := range entries {
		wg.Add(1)
		go func(i int, e Entry) {
			defer wg.Done()
			st, ok := m.Prober.Probe(ctx, e)
			results[i] = result{st, ok}
		}(i, e)
	}
	wg.Wait()
	out := make(map[string]AgentState, len(entries))
	for _, r := range results {
		if r.ok {
			out[r.st.Agent] = r.st
		}
	}
	return out
}

// refresh probes every agent and stores the result. A previous LastUpdate
// carries over. It returns the agents whose newer version is new to the
// notified set, when OnNewVersion is set.
func (m *Manager) refresh(ctx context.Context) []AgentState {
	probed := m.probeAll(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.loadLocked()
	for agent, old := range st.Agents {
		if cur, ok := probed[agent]; ok {
			cur.LastUpdate = old.LastUpdate
			probed[agent] = cur
		}
	}
	st.Agents = probed
	st.CheckedAt = m.now().UTC()
	var fresh []AgentState
	if m.OnNewVersion != nil && m.Policy.Notifies() {
		for _, a := range sortedStates(probed) {
			if a.Outdated() && st.MarkNotified(a.Agent, a.Latest) {
				fresh = append(fresh, a)
			}
		}
	}
	m.saveLocked()
	return fresh
}

func sortedStates(in map[string]AgentState) []AgentState {
	out := make([]AgentState, 0, len(in))
	for _, a := range in {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Agent < out[j].Agent })
	return out
}

// ensureProbed refreshes when force is set or the host was never probed.
func (m *Manager) ensureProbed(ctx context.Context, force bool) {
	m.mu.Lock()
	never := m.loadLocked().CheckedAt.IsZero()
	m.mu.Unlock()
	if force || never {
		for _, a := range m.refresh(ctx) {
			m.OnNewVersion(a)
		}
	}
}

// Status reports every catalog agent. refresh probes first; a host that was
// never probed is probed regardless.
func (m *Manager) Status(ctx context.Context, refresh bool) ([]AgentStatus, error) {
	m.ensureProbed(ctx, refresh)
	sessions := m.live(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.loadLocked()
	out := make([]AgentStatus, 0, len(m.entries()))
	for _, e := range m.entries() {
		a, ok := st.Agents[e.Agent]
		if !ok {
			out = append(out, AgentStatus{AgentState: AgentState{Agent: e.Agent}, Status: StatusNotInstalled, Held: m.Policy.Held(e.Agent)})
			continue
		}
		row := AgentStatus{AgentState: a, Held: m.Policy.Held(e.Agent)}
		row.Status, row.Reason = m.statusLocked(a, sessions)
		out = append(out, row)
	}
	return out, nil
}

func (m *Manager) statusLocked(a AgentState, sessions []LiveSession) (status, reason string) {
	if m.updating[a.Agent] {
		return StatusUpdating, ""
	}
	if lu := a.LastUpdate; lu != nil && !lu.OK && a.Outdated() {
		if strings.HasPrefix(lu.Log, "needs root") {
			return StatusNeedsRoot, lu.Log
		}
		return StatusFailed, lu.Log
	}
	switch {
	case a.Outdated():
		if ok, why := Gate(a.Agent, a.Method, sessions); !ok {
			return why, why
		}
		return StatusAvailable, ""
	case a.Latest == "":
		return StatusLatestUnknown, a.Error
	default:
		return StatusCurrent, ""
	}
}

// Request starts updates in the background. With all it takes every
// outdated installed agent; otherwise the named agent, outdated or not. A
// target that is busy, not installed or already updating is refused with a
// reason. An unknown agent is an error.
func (m *Manager) Request(ctx context.Context, agent string, all bool) ([]Outcome, error) {
	m.ensureProbed(ctx, false)
	var targets []string
	if all {
		m.mu.Lock()
		for _, a := range sortedStates(m.loadLocked().Agents) {
			if a.Outdated() {
				targets = append(targets, a.Agent)
			}
		}
		m.mu.Unlock()
	} else {
		agent = strings.TrimSpace(agent)
		if _, ok := m.entry(agent); !ok {
			return nil, fmt.Errorf("unknown agent %q", agent)
		}
		targets = []string{agent}
	}
	sessions := m.live(ctx)
	out := make([]Outcome, 0, len(targets))
	for _, t := range targets {
		out = append(out, m.start(t, sessions, true))
	}
	return out, nil
}

// start checks one target and, when it may run, applies it. background runs
// the update in a goroutine; otherwise start returns after it finishes.
func (m *Manager) start(agent string, sessions []LiveSession, background bool) Outcome {
	e, _ := m.entry(agent)
	m.mu.Lock()
	st := m.loadLocked()
	a, installed := st.Agents[agent]
	switch {
	case m.updating[agent]:
		m.mu.Unlock()
		return Outcome{Agent: agent, Reason: StatusUpdating}
	case !installed:
		m.mu.Unlock()
		return Outcome{Agent: agent, Reason: StatusNotInstalled}
	}
	if ok, why := Gate(agent, a.Method, sessions); !ok {
		m.mu.Unlock()
		return Outcome{Agent: agent, Reason: why}
	}
	m.updating[agent] = true
	m.wg.Add(1)
	m.mu.Unlock()
	// A background start answers agents.update; a foreground start is
	// auto mode applying a due update.
	if background {
		go m.apply(e, a, false)
	} else {
		m.apply(e, a, true)
	}
	return Outcome{Agent: agent, Accepted: true}
}

func (m *Manager) baseCtx() context.Context {
	if m.BaseCtx != nil {
		return m.BaseCtx
	}
	return context.Background()
}

// apply runs one update, re-probes the agent and records the attempt. The
// gate is checked again under applyMu, since a queued update may start
// after a session began working.
func (m *Manager) apply(e Entry, before AgentState, auto bool) {
	defer m.wg.Done()
	defer func() {
		m.mu.Lock()
		delete(m.updating, e.Agent)
		m.mu.Unlock()
	}()
	ctx := m.baseCtx()
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	if ok, why := Gate(e.Agent, before.Method, m.live(ctx)); !ok {
		m.logf("agent updates: %s %s", e.Agent, why)
		return
	}
	res := m.Runner.Run(ctx, e.Agent, e.UpdateCommand(before.Method))
	after, installed := m.Prober.Probe(ctx, e)
	lu := &LastUpdate{At: m.now().UTC(), From: before.Installed, Target: before.Latest, OK: res.OK}
	if res.Err != nil {
		lu.Log = res.Err.Error()
	}
	if installed {
		lu.To = after.Installed
	}
	m.mu.Lock()
	st := m.loadLocked()
	if installed {
		after.LastUpdate = lu
		st.Agents[e.Agent] = after
	} else {
		before.LastUpdate = lu
		st.Agents[e.Agent] = before
	}
	m.saveLocked()
	m.mu.Unlock()
	if m.OnApplied != nil {
		m.OnApplied(Applied{Agent: e.Agent, From: lu.From, To: lu.To, OK: res.OK, Err: lu.Log, Auto: auto})
	}
	if !res.OK {
		m.logf("agent updates: %s failed: %s", e.Agent, lu.Log)
		return
	}
	m.logf("agent updates: %s %s -> %s", e.Agent, lu.From, lu.To)
	if m.AfterUpdate != nil {
		m.AfterUpdate(e.Agent)
	}
}

// Wait blocks until every update started so far has finished.
func (m *Manager) Wait() { m.wg.Wait() }

// Sweep probes every agent, hands newly seen versions to OnNewVersion, and in
// auto mode applies due updates. It does nothing when the mode is off.
func (m *Manager) Sweep(ctx context.Context) {
	if !m.Policy.Probes() {
		return
	}
	m.ensureProbed(ctx, true)
	m.ApplyDue(ctx)
}

// ApplyDue applies, one at a time, every outdated agent that auto mode may
// update now and the gate allows. A failed update is not retried until a
// newer version appears.
func (m *Manager) ApplyDue(ctx context.Context) {
	if m.Policy.Mode != ModeAuto {
		return
	}
	now := m.now()
	m.mu.Lock()
	var due []string
	for _, a := range sortedStates(m.loadLocked().Agents) {
		if !a.Outdated() || !m.Policy.AutoApply(a.Agent, now) {
			continue
		}
		if lu := a.LastUpdate; lu != nil && !lu.OK && lu.Target == a.Latest {
			continue
		}
		due = append(due, a.Agent)
	}
	m.mu.Unlock()
	for _, agent := range due {
		if ctx.Err() != nil {
			return
		}
		if o := m.start(agent, m.live(ctx), false); !o.Accepted {
			m.logf("agent updates: %s %s", agent, o.Reason)
		}
	}
}

// Run probes FirstProbeDelay after start and then every CheckEvery. In auto
// mode it also retries due updates every ApplyEvery. It returns when ctx is
// done, and at once when the mode is off.
func (m *Manager) Run(ctx context.Context) {
	if !m.Policy.Probes() {
		return
	}
	first := m.FirstProbeDelay
	if first <= 0 {
		first = DefaultFirstProbeDelay
	}
	every := m.CheckEvery
	if every <= 0 {
		every = DefaultCheckEvery
	}
	probe := time.NewTimer(first)
	defer probe.Stop()
	var applyC <-chan time.Time
	if m.Policy.Mode == ModeAuto {
		applyEvery := m.ApplyEvery
		if applyEvery <= 0 {
			applyEvery = DefaultApplyEvery
		}
		t := time.NewTicker(applyEvery)
		defer t.Stop()
		applyC = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-probe.C:
			m.Sweep(ctx)
			probe.Reset(every)
		case <-applyC:
			m.ApplyDue(ctx)
		}
	}
}
