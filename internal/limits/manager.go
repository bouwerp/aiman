package limits

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// runWaitDelay bounds how long a killed command's grandchildren may hold its
// stdout open before Env.Run gives up on them.
const runWaitDelay = 2 * time.Second

// Manager caches one reading per source and runs the sources that are due.
// Nothing runs on a timer: only a Snapshot call starts work.
type Manager struct {
	sources []Source
	now     func() time.Time

	// runCtx ends at Close, so a running source stops with serve.
	runCtx context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	slots  []slot
	closed bool
}

// slot is one source's cached reading and run state.
type slot struct {
	reading Reading
	has     bool
	running bool
	ranAt   time.Time
}

// NewManager returns a Manager over sources. Readings keep the sources' order.
func NewManager(sources []Source, now func() time.Time) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		sources: sources,
		now:     now,
		runCtx:  ctx,
		cancel:  cancel,
		slots:   make([]slot, len(sources)),
	}
}

// Snapshot returns the cached readings at once and starts whatever is due.
// force skips the age check and joins a run already in flight.
func (m *Manager) Snapshot(force bool) Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	out := Snapshot{Readings: make([]Reading, 0, len(m.slots))}
	for i := range m.slots {
		sl := &m.slots[i]
		if m.due(i, force, now) {
			m.start(i)
		}
		out.Refreshing = out.Refreshing || sl.running
		if sl.has {
			out.Readings = append(out.Readings, asOf(sl.reading, now))
		}
	}
	return out
}

// Reading returns one provider's cached reading and starts its source when due,
// never forced and never waiting. Session reads call this as often as clients
// poll, so the source's minimum age is what bounds its runs. ok is false until
// the first run finishes, and for a source that does not apply here and says
// nothing.
func (m *Manager) Reading(provider string) (reading Reading, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	for i := range m.slots {
		if m.sources[i].Name() != provider {
			continue
		}
		if m.due(i, false, now) {
			m.start(i)
		}
		sl := &m.slots[i]
		if !sl.has || (sl.reading.State == StateUnavailable && sl.reading.Reason == "") {
			return Reading{}, false
		}
		return asOf(sl.reading, now), true
	}
	return Reading{}, false
}

// start runs source i in the background. The caller holds mu.
func (m *Manager) start(i int) {
	m.slots[i].running = true
	m.wg.Add(1)
	go m.run(i)
}

// due reports whether source i should start now. The caller holds mu.
func (m *Manager) due(i int, force bool, now time.Time) bool {
	sl := &m.slots[i]
	if m.closed || sl.running {
		return false
	}
	return force || !sl.has || now.Sub(sl.ranAt) >= m.sources[i].MinAge()
}

func (m *Manager) run(i int) {
	defer m.wg.Done()
	src := m.sources[i]
	ctx, cancel := context.WithTimeout(m.runCtx, src.Timeout())
	defer cancel()
	next := src.Read(ctx)
	if next.Provider == "" {
		next.Provider = src.Name()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sl := &m.slots[i]
	sl.reading = keepNumbers(sl.reading, next)
	sl.has = true
	sl.running = false
	sl.ranAt = m.now()
}

// Close stops running sources, waits for them and refuses new runs.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.cancel()
	m.wg.Wait()
}

// keepNumbers applies a failed read to the previous reading: the failure is
// reported, while the old numbers stay with their real ObservedAt. Unavailable
// is not a failure: the source no longer applies, so its old numbers go too.
func keepNumbers(prev, next Reading) Reading {
	hasNumbers := len(prev.Windows) > 0 || len(prev.Balances) > 0 || prev.Spend != ""
	if next.State != StateUnknown || !hasNumbers {
		return next
	}
	prev.State = next.State
	prev.Reason = next.Reason
	return prev
}

// asOf copies r for a caller, with any window past its reset reported as reset.
// A percent from before the reset is never shown as current.
func asOf(r Reading, now time.Time) Reading {
	r.Balances = slices.Clone(r.Balances)
	r.Windows = slices.Clone(r.Windows)
	for i := range r.Windows {
		w := &r.Windows[i]
		if !w.ResetsAt.IsZero() && !now.Before(w.ResetsAt) {
			w.Reset = true
			w.UsedPercent = 0
		}
	}
	return r
}

// DefaultSources lists every provider's source.
func DefaultSources(env Env) []Source {
	return []Source{
		NewCodexSource(env),
		NewClaudeSource(env),
		NewGrokSource(env),
		NewOpenRouterSource(env),
		NewDeepSeekSource(env),
		NewMuseSource(env),
		NewCopilotSource(env),
		NewOpenCodeSource(env),
	}
}

// HostEnv is the Env of the host serve runs on.
func HostEnv() Env {
	home, _ := os.UserHomeDir()
	return Env{
		Home:   home,
		Now:    time.Now,
		Getenv: os.Getenv,
		Run: func(ctx context.Context, name string, args ...string) (string, int, error) {
			return runCommand(ctx, resolveCommand(home, name), args...)
		},
	}
}

// resolveCommand prefers the user npm prefix, which serve's other agent
// commands put first on PATH. A bare name otherwise resolves through PATH.
func resolveCommand(home, name string) string {
	if home == "" {
		return name
	}
	path := filepath.Join(home, ".npm-global", "bin", name)
	if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
		return path
	}
	return name
}

// runCommand returns stdout and the exit code. A non-zero exit is not an error;
// a command that could not start or that ran out of time is.
func runCommand(ctx context.Context, name string, args ...string) (string, int, error) {
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	cmd.WaitDelay = runWaitDelay
	err := cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return stdout.String(), -1, ctxErr
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return stdout.String(), exit.ExitCode(), nil
	}
	if err != nil {
		return stdout.String(), -1, err
	}
	return stdout.String(), 0, nil
}
