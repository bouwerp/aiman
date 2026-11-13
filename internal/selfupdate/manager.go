package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/infra/remotesvc"
)

// Fetcher asks GitHub for the latest release. changed is false on a 304 (the
// stored etag still holds); rel is then zero.
type Fetcher interface {
	Latest(ctx context.Context, etag string) (rel Release, changed bool, err error)
}

// Runner starts the update script detached and makes sure a Result is written
// when it ends. It returns the wrapper's PID once the script is started.
type Runner interface {
	Start(tag, script string) (pid int, err error)
}

// Status values a Manager reports.
const (
	StatusCurrent   = "current"
	StatusAvailable = "available"
	StatusNotReady  = "not_ready"
	StatusDeferred  = "deferred"
	StatusUpdating  = "updating"
	StatusFailed    = "failed"
	StatusHeld      = "held"
	StatusUnknown   = "unknown"
)

// Status is what the manager knows about this host's release.
type Status struct {
	Version     string
	Latest      string
	LatestReady bool
	Mode        Mode
	Status      string
	CheckedAt   time.Time
	Attempt     *Attempt
	// Operation is the last daemon restart, apart from Attempt.
	Operation *Attempt
	// Gateway is the host's aiman-gateway against serve; nil when
	// ReadGateway is unset.
	Gateway *GatewayStatus
}

// Outcome answers Request.
type Outcome struct {
	Accepted bool
	Reason   string
	Target   string
}

// Manager keeps serve on the latest ready release. Only Version, Fetcher,
// Runner, StatePath, ResultPath and Policy are needed; the rest has defaults.
type Manager struct {
	Version string
	Fetcher Fetcher
	Runner  Runner
	// Script builds the update script; nil means
	// remotesvc.UpdateScript(remotesvc.KindServe, tag).
	Script                func(tag string) string
	StatePath, ResultPath string
	// LogPath is the runner's log; its last line explains a failed install.
	LogPath string
	Policy  Policy
	// Busy counters defer an install while any is non-zero. Busy[0] is the
	// long calls in flight, the only one still honoured after MaxDefer.
	Busy         []func() int
	OnNewVersion func(Release)
	OnApplied    func(Attempt)
	Logf         func(string, ...any)
	Now          func() time.Time
	// FirstProbeDelay and DeferRetry pace Run; zero means the defaults.
	FirstProbeDelay, DeferRetry time.Duration
	// ReadGateway reads the host's aiman-gateway. nil means no gateway
	// handling: Status.Gateway stays nil and no repair runs.
	ReadGateway func(context.Context) (Gateway, error)
	// GatewayScript builds the gateway repair script; nil means
	// remotesvc.GatewayRepairScript(tag).
	GatewayScript func(tag string) string
	// OnGatewayBehind reports, in notify mode, a gateway older than serve
	// once per gateway version and serve tag.
	OnGatewayBehind func(gatewayVersion, serveTag string)
	// OpsDir holds operation results; empty refuses StartOperation.
	OpsDir string
	// OpScript builds operation name's script; nil means
	// remotesvc.RestartScript for the name's daemon.
	OpScript func(name string) string

	// opMu serialises settle, poll and apply, so two scripts never start.
	opMu sync.Mutex
	// mu guards the deferral of the current tag.
	mu         sync.Mutex
	deferTag   string
	deferSince time.Time
	// gwMu guards the gateway read cache and the read in flight.
	gwMu     sync.Mutex
	gwCache  *gatewayRead
	gwFlight chan struct{}
}

// events are the callbacks a locked operation queued; they run after the
// locks are released.
type events struct {
	applied []Attempt
	release *Release
	// gatewayBehind is {gateway version, serve tag} for OnGatewayBehind.
	gatewayBehind *[2]string
}

func (m *Manager) fire(ev events) {
	if ev.release != nil && m.OnNewVersion != nil {
		m.OnNewVersion(*ev.release)
	}
	if g := ev.gatewayBehind; g != nil && m.OnGatewayBehind != nil {
		m.OnGatewayBehind(g[0], g[1])
	}
	if m.OnApplied != nil {
		for _, a := range ev.applied {
			m.OnApplied(a)
		}
	}
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
	}
}

func (m *Manager) script(tag string) string {
	if m.Script != nil {
		return m.Script(tag)
	}
	return remotesvc.UpdateScript(remotesvc.KindServe, tag)
}

// dev reports a build with no release number, which never updates itself.
func (m *Manager) dev() bool {
	_, ok := remotesvc.ReleaseTag(m.Version)
	return !ok
}

func (m *Manager) load() State {
	s, err := LoadState(m.StatePath)
	if err != nil {
		m.logf("host-update: read %s: %v", m.StatePath, err)
	}
	return s
}

func (m *Manager) save(s State) {
	if err := s.Save(m.StatePath); err != nil {
		m.logf("host-update: save %s: %v", m.StatePath, err)
	}
}

// Status reports the host's release state from the state file, and the
// gateway from a read at most gatewayCacheTTL old. It never waits for a read
// already running.
func (m *Manager) Status() Status {
	g := m.readGateway(context.Background(), false)
	return m.statusOf(m.load(), g)
}

func (m *Manager) statusOf(s State, g *gatewayRead) Status {
	v := m.judge(s, g, false)
	st := Status{
		Version:     m.Version,
		Latest:      s.Latest.Tag,
		LatestReady: s.Latest.Ready,
		Mode:        m.Policy.mode(),
		Status:      v.status,
		CheckedAt:   s.CheckedAt,
		Attempt:     s.Attempt,
		Operation:   s.Operation,
		Gateway:     m.gatewayStatus(s, g),
	}
	if v.apply && v.kind != AttemptKindGateway && m.Policy.mode() == ModeAuto && m.deferring(v.tag) {
		st.Status = StatusDeferred
	}
	return st
}

// verdict is what the stored state says about the latest release.
type verdict struct {
	status string
	apply  bool // a ready release may be installed, subject to the gate
	tag    string
	// kind is AttemptKindGateway when apply means a gateway repair to tag.
	kind string
	// gatewayRunning marks a gateway repair still running.
	gatewayRunning bool
}

// judge decides from the stored state and the gateway read g (nil without a
// reader). forced is host.update, which repairs the gateway inside the retry
// interval. A newer serve release always takes the serve path: its lockstep
// carries the gateway.
func (m *Manager) judge(s State, g *gatewayRead, forced bool) verdict {
	switch {
	case m.Policy.mode() == ModeOff:
		return verdict{status: StatusHeld}
	case m.dev():
		return verdict{status: StatusUnknown}
	}
	a, lat := s.Attempt, s.Latest
	if a != nil && a.Status == AttemptStarted {
		if !a.IsGateway() {
			return verdict{status: StatusUpdating, tag: a.To}
		}
		v := m.judgeServe(lat, nil)
		v.apply, v.gatewayRunning = false, true
		return v
	}
	// Serve is judged without the gateway's failures: a failed repair, or
	// (with a reader) a failed lockstep at serve's own version.
	if a.IsGateway() || (g != nil && m.gatewayFailedAt(a)) {
		a = nil
	}
	v := m.judgeServe(lat, a)
	if g == nil {
		return v
	}
	if _, _, newer := remotesvc.Outdated(m.Version, lat.Tag); newer {
		return v
	}
	return m.judgeGateway(s, g, v, forced)
}

// judgeServe is serve's verdict against the latest release and its last
// attempt, which is not running.
func (m *Manager) judgeServe(lat Release, a *Attempt) verdict {
	if lat.Tag == "" {
		return verdict{status: StatusUnknown}
	}
	_, _, newer := remotesvc.Outdated(m.Version, lat.Tag)
	// A failed attempt at the latest tag is retried, even at this version: a
	// failed gateway lockstep leaves serve on the tag and the gateway behind.
	retry := a != nil && a.Status == AttemptFailed && a.To == lat.Tag
	switch {
	case retry && m.now().Sub(a.At) < m.Policy.retryAfter():
		return verdict{status: StatusFailed, tag: lat.Tag}
	case !newer && !retry:
		return verdict{status: StatusCurrent, tag: lat.Tag}
	case !lat.Ready:
		return verdict{status: StatusNotReady, tag: lat.Tag}
	case !newer:
		return verdict{status: StatusFailed, apply: true, tag: lat.Tag}
	default:
		return verdict{status: StatusAvailable, apply: true, tag: lat.Tag}
	}
}

// Poll settles a started attempt, checks GitHub once and, in auto mode,
// installs a ready newer release unless the gate defers. It never waits on
// the gate.
func (m *Manager) Poll(ctx context.Context) Status {
	return m.poll(ctx, true)
}

func (m *Manager) poll(ctx context.Context, fetch bool) Status {
	if m.Policy.mode() == ModeOff || m.dev() {
		m.Operation()
		return m.Status()
	}
	var ev events
	g := m.readGateway(ctx, true)
	m.opMu.Lock()
	s := m.load()
	ev.add(m.settleLocked(&s))
	m.settleOpLocked(&s)
	if fetch {
		m.fetchLocked(ctx, &s)
	}
	v := m.judge(s, g, false)
	gateway := v.kind == AttemptKindGateway
	switch {
	case m.Policy.mode() == ModeNotify && gateway && v.apply:
		if key := gatewayKey(g, v.tag); s.NotifiedGateway != key {
			s.NotifiedGateway = key
			m.save(s)
			ev.gatewayBehind = &[2]string{g.gw.Version, v.tag}
			m.logf("host-update: gateway %s behind serve %s", g.gw.Version, v.tag)
		}
	case m.Policy.mode() == ModeNotify && !gateway && v.status == StatusAvailable && s.Notified != v.tag:
		s.Notified = v.tag
		m.save(s)
		rel := s.Latest
		ev.release = &rel
		m.logf("host-update: %s available", v.tag)
	case m.Policy.mode() == ModeAuto && v.apply && started(s.Operation):
		m.logf("host-update: %s waits for %s", v.tag, s.Operation.To)
	case m.Policy.mode() == ModeAuto && v.apply:
		if n := m.gate(v.tag); n > 0 {
			m.logf("host-update: %s deferred, %d busy", v.tag, n)
		} else {
			ev.add(m.applyLocked(&s, v, g))
		}
	}
	st := m.statusOf(s, g)
	m.opMu.Unlock()
	m.fire(ev)
	return st
}

func (ev *events) add(a *Attempt) {
	if a != nil {
		ev.applied = append(ev.applied, *a)
	}
}

// Request settles, polls and, in any mode but off, installs a ready newer
// release through the gate. It never waits: a busy host is refused at once.
func (m *Manager) Request(ctx context.Context) Outcome {
	switch {
	case m.Policy.mode() == ModeOff:
		return Outcome{Reason: "host updates are off on this host"}
	case m.dev():
		return Outcome{Reason: fmt.Sprintf("development build %q does not update itself", m.Version)}
	}
	var ev events
	g := m.readGateway(ctx, true)
	m.opMu.Lock()
	s := m.load()
	ev.add(m.settleLocked(&s))
	m.settleOpLocked(&s)
	m.fetchLocked(ctx, &s)
	v := m.judge(s, g, true)
	out := Outcome{Target: v.tag}
	switch {
	case v.apply && started(s.Operation):
		out.Reason = fmt.Sprintf("%s is running; try again shortly", s.Operation.To)
	case !v.apply:
		out.Reason = m.refusal(v, s)
	default:
		if n := m.gate(v.tag); n > 0 {
			out.Reason = fmt.Sprintf("serve is busy (%d long calls, logins, handovers or agent updates); try again shortly", n)
		} else if a := m.applyLocked(&s, v, g); a != nil {
			ev.add(a)
			out.Reason = a.Error
		} else {
			out.Accepted = true
			if v.kind == AttemptKindGateway {
				out.Reason = ReasonGateway
			}
		}
	}
	m.opMu.Unlock()
	m.fire(ev)
	return out
}

func (m *Manager) refusal(v verdict, s State) string {
	if v.gatewayRunning {
		return fmt.Sprintf("a gateway update to %s is already running", s.Attempt.To)
	}
	switch v.status {
	case StatusCurrent:
		return fmt.Sprintf("%s is the latest release", m.Version)
	case StatusNotReady:
		return fmt.Sprintf("%s is not ready yet (no checksums.txt)", v.tag)
	case StatusUpdating:
		return fmt.Sprintf("an update to %s is running", v.tag)
	case StatusFailed:
		return fmt.Sprintf("the install of %s failed at %s; it is retried %s after that",
			v.tag, s.Attempt.At.UTC().Format(time.RFC3339), m.Policy.retryAfter())
	default:
		return "the latest release is unknown; GitHub could not be reached"
	}
}

func (m *Manager) fetchLocked(ctx context.Context, s *State) {
	if m.Fetcher == nil {
		return
	}
	rel, changed, err := m.Fetcher.Latest(ctx, s.ETag)
	if err != nil {
		m.logf("host-update: %v", err)
		return
	}
	s.CheckedAt = m.now()
	if changed {
		if rel.Tag != s.Latest.Tag || rel.Ready != s.Latest.Ready {
			m.logf("host-update: latest %s (ready %v)", rel.Tag, rel.Ready)
		}
		s.Latest = rel
		s.ETag = rel.ETag
	}
	m.save(*s)
}

// gate returns the busy sum that defers an install of tag, zero when it may
// go ahead. After MaxDefer of deferring one tag only Busy[0] counts.
func (m *Manager) gate(tag string) int {
	now := m.now()
	m.mu.Lock()
	if m.deferTag != tag {
		m.deferTag, m.deferSince = tag, time.Time{}
	}
	counters := m.Busy
	if !m.deferSince.IsZero() && now.Sub(m.deferSince) >= MaxDefer {
		counters = counters[:min(1, len(counters))]
	}
	m.mu.Unlock()
	sum := 0
	for _, c := range counters {
		if c != nil {
			sum += c()
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if sum > 0 && m.deferSince.IsZero() {
		m.deferSince = now
	}
	if sum == 0 {
		m.deferTag, m.deferSince = "", time.Time{}
	}
	return sum
}

func (m *Manager) deferring(tag string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deferTag == tag && !m.deferSince.IsZero()
}

// applyLocked saves a started attempt for verdict v, starts its script (the
// serve update, or the gateway repair) and saves the PID. It returns the
// attempt only when it failed to start, for OnApplied.
func (m *Manager) applyLocked(s *State, v verdict, g *gatewayRead) *Attempt {
	tag := v.tag
	if err := os.Remove(m.ResultPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		m.logf("host-update: remove %s: %v", m.ResultPath, err)
	}
	a := &Attempt{At: m.now(), From: m.Version, To: tag, Status: AttemptStarted}
	script := m.script
	what := tag
	if v.kind == AttemptKindGateway {
		a.Kind, script, what = AttemptKindGateway, m.gatewayScript, "gateway "+tag
		if g != nil {
			a.From = g.gw.Version
		}
	}
	s.Attempt = a
	if err := s.Save(m.StatePath); err != nil {
		// Without a saved attempt nothing could settle the install.
		a.Status, a.Error = AttemptFailed, "save state: "+err.Error()
		m.logf("host-update: %s not installed: %s", what, a.Error)
		out := *a
		return &out
	}
	pid, err := m.Runner.Start(tag, script(tag))
	if err != nil {
		a.Status, a.Error = AttemptFailed, "start update script: "+err.Error()
		m.save(*s)
		m.logf("host-update: %s not installed: %s", what, a.Error)
		out := *a
		return &out
	}
	a.PID = pid
	m.save(*s)
	m.logf("host-update: installing %s (pid %d)", what, pid)
	go m.watch()
	return nil
}
