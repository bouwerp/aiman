package harness

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// Defaults of the manager's options.
const (
	DefaultMaxRuns     = 2
	DefaultIdleTimeout = 2 * time.Hour
	// ReadyTimeout bounds each service's ready probe.
	ReadyTimeout = 60 * time.Second
	// StartTimeout bounds a whole start, build included.
	StartTimeout = 30 * time.Minute
	// CheckTimeout bounds one check command.
	CheckTimeout = 5 * time.Minute
	// stopTimeout bounds compose down.
	stopTimeout = 2 * time.Minute
)

// Recorder writes one ledger row.
type Recorder func(sessionID string, actor domain.EventActor, kind domain.EventKind, payload map[string]any)

// Options configure a Manager.
type Options struct {
	// Dir is ~/.aiman/harness: <session>/<run>/ under it.
	Dir string
	// AimanBin is the static aiman binary the edge runs, mounted read-only.
	AimanBin string
	// UID and GID run the app and the edge.
	UID, GID int
	// EdgeImage is the edge's base image; empty is EdgeImage.
	EdgeImage string
	// AllowedImages is harness.allowed_images; nil is DefaultAllowedImages.
	AllowedImages []string
	// ServiceMemory and RunMemory are harness.service_memory and
	// harness.run_memory in bytes; zero takes the defaults. The caller keeps
	// RunMemory at or below resources.session.memory_high.
	ServiceMemory, RunMemory int64
	// MaxRuns is harness.max_runs; zero is DefaultMaxRuns.
	MaxRuns int
	// IdleTimeout is harness.idle_timeout; zero is DefaultIdleTimeout.
	IdleTimeout time.Duration
	Docker      Docker
	// Images vets every image of a run, the edge's as builtin, fetching what
	// the host lacks, and answers its image ID; nil keeps the older rule:
	// listed in AllowedImages (the edge excepted) and already on the host.
	Images ImageVetter
	// Edge reads the edge's control socket; nil is SocketEdge.
	Edge EdgeControl
	// AppImage picks the app's image from the exported tree and the repo's
	// review.image override; it must be set.
	AppImage func(src, override string) (string, error)
	// DebugAdapters serve debug runs, one per app.debug.lang; a language
	// without one is refused with CodeDebugUnsupported.
	DebugAdapters []DebugAdapter
	// Record writes ledger rows; nil records nothing.
	Record Recorder
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// ImageRequest asks for one image of a run.
type ImageRequest struct {
	Ref string
	// Session is the run's session; Operator is set when the operator
	// started the run.
	Session  string
	Operator bool
	// Builtin marks an image aiman's own code pins by digest (the edge):
	// the release is its approval, so a clean or high-only scan approves it
	// without a gate; a block still refuses.
	Builtin bool
}

// ImageVetter answers the engine's image ID of an approved image, or a
// *Error with CodeImagePending, CodeImageBlocked, CodeImageResolveFailed or
// CodeImageTooLarge. A ref it has not seen starts vetting.
type ImageVetter interface {
	Ensure(ctx context.Context, req ImageRequest) (string, error)
}

// Manager owns the harness runs of a host.
type Manager struct {
	opts Options

	mu   sync.Mutex
	live map[string]*liveRun
	// nets are the subnet slots live runs hold.
	nets   map[int]bool
	closed bool
	wg     sync.WaitGroup
}

// liveRun is a run that holds, or is starting, containers.
type liveRun struct {
	mu     sync.Mutex
	run    Run
	token  string
	net    int
	cancel context.CancelFunc
	done   chan struct{}
	// stopping is set by the first of Down and a failed start; the other
	// leaves the run alone.
	stopping bool
	// debug is the adapter of a debug run.
	debug DebugAdapter
}

// NewManager checks opts and returns a manager with no live runs.
func NewManager(opts Options) (*Manager, error) {
	if opts.Dir == "" || opts.Docker == nil || opts.AppImage == nil {
		return nil, errors.New("harness manager: Dir, Docker and AppImage are required")
	}
	if opts.EdgeImage == "" {
		opts.EdgeImage = EdgeImage
	}
	if opts.AllowedImages == nil {
		opts.AllowedImages = DefaultAllowedImages
	}
	if opts.MaxRuns <= 0 {
		opts.MaxRuns = DefaultMaxRuns
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = DefaultIdleTimeout
	}
	if opts.Edge == nil {
		opts.Edge = SocketEdge{}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return nil, err
	}
	return &Manager{opts: opts, live: map[string]*liveRun{}, nets: map[int]bool{}}, nil
}

// Close stops every start in flight and waits for it. Running containers stay;
// the next serve takes them down with CleanStale.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	for _, lr := range m.live {
		if lr.cancel != nil {
			lr.cancel()
		}
	}
	m.mu.Unlock()
	m.wg.Wait()
}

func (m *Manager) record(session string, actor domain.EventActor, kind domain.EventKind, payload map[string]any) {
	if m.opts.Record != nil {
		m.opts.Record(session, actor, kind, payload)
	}
}

// UpRequest asks for one run.
type UpRequest struct {
	Session  string
	Repo     string // the session's checkout root
	SHA      string // the commit to run; empty is HEAD
	Scenario string
	// Dirty runs the working tree; SkipCleanCheck runs SHA whatever the
	// working tree holds (a review session's staged PR).
	Dirty          bool
	SkipCleanCheck bool
	// Override is repos.<slug>.harness as YAML; AppImageOverride is
	// repos.<slug>.review.image.
	Override         []byte
	AppImageOverride string
	// Debug runs the app under the debug adapter of app.debug.lang.
	Debug bool
	Actor domain.EventActor
}

// Up checks and reserves a run, exports its tree and starts it in the
// background. The returned run is starting; Wait follows it.
func (m *Manager) Up(ctx context.Context, req UpRequest) (Run, error) {
	if !safeIDRe.MatchString(req.Session) {
		return Run{}, fmt.Errorf("harness: session id %q is not usable as a directory name", req.Session)
	}
	if err := m.refuseRunning(req.Session); err != nil {
		return Run{}, err
	}
	loaded, err := m.load(ctx, req)
	if err != nil {
		return Run{}, err
	}
	f := loaded.File
	sc, ok := f.Scenario(req.Scenario)
	if !ok {
		return Run{}, &Error{Code: CodeUnknownScenario, Msg: fmt.Sprintf("no scenario %q in the harness file", req.Scenario)}
	}
	if err := checkFixtures(sc); err != nil {
		return Run{}, err
	}
	if err := f.CheckLimits(sc.ID, LimitOptions{ServiceMemory: m.opts.ServiceMemory, RunMemory: m.opts.RunMemory}); err != nil {
		return Run{}, err
	}
	var adapter DebugAdapter
	var launch DebugLaunch
	if req.Debug {
		if adapter, err = DebugAdapterFor(f, m.opts.DebugAdapters); err != nil {
			return Run{}, err
		}
		if launch, err = adapter.Launch(f); err != nil {
			return Run{}, err
		}
	}
	lr, err := m.reserve(req.Session)
	if err != nil {
		return Run{}, err
	}
	if adapter != nil {
		lr.debug, lr.run.Debug, lr.run.DebugLaunch = adapter, true, &launch
	}
	run, err := m.prepare(ctx, req, loaded, sc, lr)
	if err != nil {
		m.release(lr)
		_ = os.RemoveAll(m.runDir(req.Session, lr.run.ID))
		return Run{}, err
	}
	m.record(run.Session, req.Actor, domain.EventHarnessStarted, map[string]any{
		"run": run.ID, "scenario": run.Scenario, "sha": run.SHA, "dirty": run.Dirty,
		"diff_sha256": run.DiffSHA256, "source": string(run.Source), "network": run.Network, "debug": run.Debug,
	})
	startCtx, cancel := context.WithTimeout(context.Background(), StartTimeout)
	lr.mu.Lock()
	lr.cancel = cancel
	lr.mu.Unlock()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer cancel()
		defer close(lr.done)
		m.start(startCtx, lr, f, sc)
	}()
	return run, nil
}

func (m *Manager) refuseRunning(session string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, lr := range m.live {
		if lr.run.Session == session {
			return &Error{Code: CodeRunning, Msg: fmt.Sprintf("session %s already has run %s; take it down first", session, lr.run.ID)}
		}
	}
	return nil
}

// load reads the harness for the request: the working tree for a dirty run,
// otherwise the commit, refusing a dirty tree unless SkipCleanCheck is set.
func (m *Manager) load(ctx context.Context, req UpRequest) (*Loaded, error) {
	if req.Dirty {
		return LoadDirty(ctx, req.Repo, req.Override)
	}
	if !req.SkipCleanCheck {
		paths, err := dirtyPaths(ctx, req.Repo)
		if err != nil {
			return nil, err
		}
		if len(paths) > 0 {
			return nil, &Error{Code: CodeDirty, Paths: paths, Msg: "the worktree has uncommitted or untracked changes: commit first, or pass --dirty"}
		}
	}
	sha := req.SHA
	if sha == "" {
		sha = "HEAD"
	}
	return LoadAtSHA(ctx, req.Repo, sha, req.Override)
}

// checkFixtures refuses SQLite fixtures, which need the database browser's
// reader container.
func checkFixtures(sc *Scenario) error {
	for i, fx := range sc.Fixtures {
		if fx.SQLite != "" {
			return invalid(fmt.Sprintf("scenario %s fixtures[%d]: sqlite fixtures are not supported yet", sc.ID, i))
		}
	}
	return nil
}

// reserve takes a live slot and a subnet for a new run of session.
func (m *Manager) reserve(session string) (*liveRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("harness manager is closed")
	}
	for _, lr := range m.live {
		if lr.run.Session == session {
			return nil, &Error{Code: CodeRunning, Msg: fmt.Sprintf("session %s already has run %s", session, lr.run.ID)}
		}
	}
	if len(m.live) >= m.opts.MaxRuns {
		return nil, &Error{Code: CodeBusy, Msg: fmt.Sprintf("the host already has %d harness runs (harness.max_runs); wait for one to stop", len(m.live))}
	}
	id, err := randomHex(6)
	if err != nil {
		return nil, err
	}
	lr := &liveRun{run: Run{ID: id, Session: session, State: StateStarting}, net: m.takeNetLocked(-1), done: make(chan struct{})}
	m.live[id] = lr
	return lr, nil
}

func (m *Manager) release(lr *liveRun) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.live, lr.run.ID)
	delete(m.nets, lr.net)
}

// netSlots is how many /24 slots of netBase runs may take.
const netSlots = 256

// takeNetLocked takes the first free subnet slot after prev. Callers hold mu.
func (m *Manager) takeNetLocked(prev int) int {
	for i := 1; i <= netSlots; i++ {
		n := (prev + i + netSlots) % netSlots
		if !m.nets[n] {
			m.nets[n] = true
			return n
		}
	}
	return (prev + 1) % netSlots
}

// nextNet gives up lr's subnet slot for the next free one.
func (m *Manager) nextNet(lr *liveRun) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old := lr.net
	lr.net = m.takeNetLocked(old)
	delete(m.nets, old)
}

// prepare writes the run directory: the exported tree, the token and run.json.
func (m *Manager) prepare(ctx context.Context, req UpRequest, l *Loaded, sc *Scenario, lr *liveRun) (Run, error) {
	now := m.opts.Now()
	run := lr.run
	run.Scenario, run.Title = sc.ID, sc.Title
	run.SHA, run.Dirty, run.DiffSHA256, run.Source = l.SHA, l.Dirty, l.DiffSHA256, l.Source
	run.Network = "offline"
	run.Services = append([]string(nil), sc.Services...)
	run.StartedAt, run.LastUsed = now, now
	run.Ports, run.Steps, run.Checks = []RunPort{}, stepViews(sc), checkRuns(sc)
	dir := m.runDir(req.Session, run.ID)
	for _, d := range []string{"src", "out", "sandbox", "control", "logs"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			return Run{}, err
		}
	}
	src := filepath.Join(dir, "src")
	var err error
	if l.Dirty {
		err = exportWorkingTree(ctx, req.Repo, src)
	} else {
		err = exportCommit(ctx, req.Repo, l.SHA, src)
	}
	if err != nil {
		return Run{}, fmt.Errorf("export the tree: %w", err)
	}
	if run.Images, err = m.images(ctx, req, l.File, sc, src); err != nil {
		return Run{}, err
	}
	token, err := randomHex(32)
	if err != nil {
		return Run{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "control", ControlTokenName), []byte(token), 0o600); err != nil {
		return Run{}, err
	}
	lr.mu.Lock()
	lr.run, lr.token = run, token
	lr.mu.Unlock()
	if err := m.writeRun(run); err != nil {
		return Run{}, err
	}
	return run, nil
}

func stepViews(sc *Scenario) []StepView {
	out := make([]StepView, 0, len(sc.Steps))
	for i, st := range sc.Steps {
		out = append(out, StepView{N: i + 1, Do: st.Do, Expect: st.Expect})
	}
	return out
}

func checkRuns(sc *Scenario) []CheckRun {
	out := make([]CheckRun, 0, len(sc.Checks))
	for _, c := range sc.Checks {
		out = append(out, CheckRun{Name: c.Name, Run: c.Run})
	}
	return out
}

// images resolves every image the run starts to its local image ID. With an
// ImageVetter, every image goes through vetting, the edge image as builtin
// (aiman's own, pinned by digest): every one is asked for, so each unknown
// digest starts vetting at once, and the first refusal is returned. Without
// one, an app or service image must match harness.allowed_images, and every
// image must be on the host.
func (m *Manager) images(ctx context.Context, req UpRequest, f *File, sc *Scenario, src string) (map[string]string, error) {
	appRef, err := m.opts.AppImage(src, req.AppImageOverride)
	if err != nil {
		return nil, invalid(fmt.Sprintf("pick the app image: %v", err))
	}
	ids := map[string]string{}
	refs := [][2]string{{ServiceApp, appRef}}
	for _, name := range sc.Services {
		refs = append(refs, [2]string{name, f.Services[name].Image})
	}
	refs = append(refs, [2]string{ServiceEdge, m.opts.EdgeImage})
	var first error
	for _, r := range refs {
		id, err := m.image(ctx, req, r[1], r[0] == ServiceEdge)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		ids[r[0]] = id
	}
	if first != nil {
		return nil, first
	}
	return ids, nil
}

func (m *Manager) image(ctx context.Context, req UpRequest, ref string, builtin bool) (string, error) {
	if m.opts.Images != nil {
		return m.opts.Images.Ensure(ctx, ImageRequest{Ref: ref, Session: req.Session, Operator: req.Actor != domain.EventActorAgent, Builtin: builtin})
	}
	if !builtin && !ImageAllowed(ref, m.opts.AllowedImages) {
		return "", imageMissing(ref, "is not in harness.allowed_images")
	}
	id, err := m.opts.Docker.ImageID(ctx, ref)
	if errors.Is(err, ErrImageNotFound) {
		return "", imageMissing(ref, "is not on this host, and image vetting is off, so serve cannot fetch it; run docker pull "+ref+" on the host")
	}
	return id, err
}

func imageMissing(ref, why string) *Error {
	return &Error{Code: CodeImageMissing, Image: ref, Msg: fmt.Sprintf("image %s %s", ref, why)}
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Wait returns the run once it leaves starting, or after d.
func (m *Manager) Wait(ctx context.Context, id string, d time.Duration) Run {
	m.mu.Lock()
	lr := m.live[id]
	m.mu.Unlock()
	if lr != nil {
		timer := time.NewTimer(d)
		defer timer.Stop()
		for {
			r := lr.snapshot()
			if r.State != StateStarting {
				break
			}
			select {
			case <-lr.done:
			case <-timer.C:
				return lr.snapshot()
			case <-ctx.Done():
				return lr.snapshot()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	r, _ := m.Get(id)
	return r
}

func (lr *liveRun) snapshot() Run {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	return lr.run
}

// Get returns a run, live or kept.
func (m *Manager) Get(id string) (Run, error) {
	m.mu.Lock()
	lr := m.live[id]
	m.mu.Unlock()
	if lr != nil {
		return lr.snapshot(), nil
	}
	if !safeIDRe.MatchString(id) {
		return Run{}, notRunning(id)
	}
	matches, _ := filepath.Glob(filepath.Join(m.opts.Dir, "*", id, "run.json"))
	if len(matches) == 0 {
		return Run{}, notRunning(id)
	}
	return readRun(matches[0])
}

func notRunning(id string) *Error {
	return &Error{Code: CodeNotRunning, Msg: fmt.Sprintf("no harness run %s", id)}
}

// Runs lists a session's runs, newest first.
func (m *Manager) Runs(session string) ([]Run, error) {
	runs, err := m.storedRuns(session)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, r := range runs {
		if lr := m.live[r.ID]; lr != nil {
			runs[i] = lr.snapshotLocked()
		}
	}
	return runs, nil
}

func (lr *liveRun) snapshotLocked() Run {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	return lr.run
}

// LiveRun returns the session's live run.
func (m *Manager) LiveRun(session string) (Run, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, lr := range m.live {
		if lr.run.Session == session {
			return lr.snapshotLocked(), true
		}
	}
	return Run{}, false
}

// Touch marks the run used now, which holds off the idle timeout.
func (m *Manager) Touch(id string) {
	m.mu.Lock()
	lr := m.live[id]
	m.mu.Unlock()
	if lr == nil {
		return
	}
	lr.mu.Lock()
	lr.run.LastUsed = m.opts.Now()
	lr.mu.Unlock()
}

// Drop takes down the session's live run: its session is gone.
func (m *Manager) Drop(ctx context.Context, session string) {
	if r, ok := m.LiveRun(session); ok {
		if err := m.Down(ctx, r.ID, StopSessionGone); err != nil {
			log.Printf("harness: drop %s run %s: %v", session, r.ID, err)
		}
	}
}

// ReapIdle takes down every live run unused for longer than the idle timeout.
func (m *Manager) ReapIdle(ctx context.Context) {
	now := m.opts.Now()
	m.mu.Lock()
	var idle []string
	for id, lr := range m.live {
		r := lr.snapshotLocked()
		if r.State == StateReady && now.Sub(r.LastUsed) > m.opts.IdleTimeout {
			idle = append(idle, id)
		}
	}
	m.mu.Unlock()
	for _, id := range idle {
		if err := m.Down(ctx, id, StopIdle); err != nil {
			log.Printf("harness: idle run %s: %v", id, err)
		}
	}
}

// RunIdle reaps idle runs every minute until ctx ends.
func (m *Manager) RunIdle(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.ReapIdle(ctx)
		}
	}
}

// CleanStale takes down the projects of runs a previous serve left starting
// or ready, and marks them stopped.
func (m *Manager) CleanStale(ctx context.Context) {
	for _, r := range m.allStoredRuns() {
		if !r.Live() {
			continue
		}
		m.mu.Lock()
		_, live := m.live[r.ID]
		m.mu.Unlock()
		if live {
			continue
		}
		m.composeDown(ctx, r)
		m.markStopped(r, StopServeRestart)
	}
}
