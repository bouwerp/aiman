package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/shellx"
)

// netAttempts is how often a start picks another subnet when Docker says the
// one it picked overlaps an existing network.
const netAttempts = 4

// errorTailLines is how much of a failed step's output the run keeps.
const errorTailLines = 20

// subnets are slot n's inner and outer networks: 10.231.<n>.0/25 and
// 10.231.<n>.128/25.
func subnets(n int) (inner, outer netip.Prefix) {
	n = ((n % netSlots) + netSlots) % netSlots
	return netip.MustParsePrefix(fmt.Sprintf("10.231.%d.0/25", n)),
		netip.MustParsePrefix(fmt.Sprintf("10.231.%d.128/25", n))
}

// debugSubnet is slot n's debug network: 10.232.<n>.0/29, room for the app,
// an adapter container and the edge at fixed addresses.
func debugSubnet(n int) netip.Prefix {
	n = ((n % netSlots) + netSlots) % netSlots
	return netip.MustParsePrefix(fmt.Sprintf("10.232.%d.0/29", n))
}

// stepError is a failed start step.
type stepError struct {
	step    string
	service string
	exit    int
	out     string
	err     error
}

func (e *stepError) Error() string {
	if e.err != nil {
		return fmt.Sprintf("%s: %v", e.step, e.err)
	}
	return fmt.Sprintf("%s exited %d", e.step, e.exit)
}

// starter runs the start steps of one run.
type starter struct {
	m       *Manager
	lr      *liveRun
	f       *File
	sc      *Scenario
	run     Run
	dir     string
	sandbox string
	project string
	relays  []Relay
	logN    int
	// debug is the debug adapter's spec; nil for a plain run.
	debug *DebugSpec
}

// start brings the run up: networks, services and their ready probes,
// fixtures, setup, build, the app, then the edge. A failure takes the project
// down and leaves the run failed.
func (m *Manager) start(ctx context.Context, lr *liveRun, f *File, sc *Scenario) {
	run := lr.snapshot()
	dir := m.runDir(run.Session, run.ID)
	s := &starter{m: m, lr: lr, f: f, sc: sc, run: run, dir: dir, sandbox: filepath.Join(dir, "sandbox"), project: "harness-" + run.ID}
	steps := []func(context.Context) error{s.debugger, s.networks, s.services, s.fixtures, s.setup, s.build, s.app, s.edge}
	for _, step := range steps {
		if err := step(ctx); err != nil {
			m.failRun(lr, err)
			return
		}
	}
	m.readyRun(lr, s.run.Ports)
	m.checkSourcemaps(ctx, lr, filepath.Join(dir, "src"))
}

// compose runs one compose command and logs its output under logs/.
func (s *starter) compose(ctx context.Context, name string, stdin io.Reader, args ...string) (string, int, error) {
	out, exit, err := s.m.opts.Docker.Compose(ctx, ComposeCall{Dir: s.sandbox, Project: s.project, Args: args, Stdin: stdin})
	s.logN++
	slug := strings.Trim(nonSlug.ReplaceAllString(name, "-"), "-")
	_ = os.WriteFile(filepath.Join(s.dir, "logs", fmt.Sprintf("%02d-%s.log", s.logN, slug)), []byte(out), 0o600)
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	return out, exit, err
}

var nonSlug = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// must runs a compose step that has to exit 0.
func (s *starter) must(ctx context.Context, step, service string, stdin io.Reader, args ...string) error {
	out, exit, err := s.compose(ctx, step, stdin, args...)
	if err != nil || exit != 0 {
		return &stepError{step: step, service: service, exit: exit, out: out, err: err}
	}
	return nil
}

// networks writes the compose project and creates it, moving to another
// subnet slot when Docker reports an overlap.
func (s *starter) networks(ctx context.Context) error {
	var out string
	var exit int
	for range netAttempts {
		if err := s.writeProject(); err != nil {
			return &stepError{step: "compose file", err: err}
		}
		var err error
		out, exit, err = s.compose(ctx, "networks", nil, "up", "--no-start")
		if err != nil {
			return &stepError{step: "networks", exit: exit, out: out, err: err}
		}
		if exit == 0 {
			return nil
		}
		if !strings.Contains(out, "overlaps") {
			break
		}
		s.m.nextNet(s.lr)
	}
	return &stepError{step: "networks", exit: exit, out: out}
}

func (s *starter) writeProject() error {
	s.lr.mu.Lock()
	n := s.lr.net
	s.lr.mu.Unlock()
	inner, outer := subnets(n)
	services := map[string]string{}
	for _, name := range s.sc.Services {
		services[name] = s.run.Images[name]
	}
	checks := map[string]string{}
	for j, c := range s.sc.Checks {
		if c.Image != "" {
			checks[CheckService(j)] = s.run.Images[CheckService(j)]
		}
	}
	readPassword, err := randomHex(16)
	if err != nil {
		return err
	}
	p, err := Compose(s.f, RunSpec{
		RunID: s.run.ID, Scenario: s.sc.ID,
		Src: filepath.Join(s.dir, "src"), Out: filepath.Join(s.dir, "out"), ControlDir: filepath.Join(s.dir, "control"),
		AimanBin: s.m.opts.AimanBin, UID: s.m.opts.UID, GID: s.m.opts.GID,
		AppImage: s.run.Images[ServiceApp], EdgeImage: s.run.Images[ServiceEdge], ServiceImages: services,
		InnerSubnet: inner, OuterSubnet: outer, ReadPassword: readPassword, ServiceMemory: s.m.opts.ServiceMemory,
		Debug: s.debug, DebugSubnet: debugSubnet(n), AdapterImage: s.run.Images[ServiceAdapter],
		CheckImages: checks,
	})
	if err != nil {
		return err
	}
	if err := s.placeLaunch(n); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(s.sandbox, "compose.yaml"), p.YAML, 0o600); err != nil {
		return err
	}
	for rel, b := range p.Files {
		path := filepath.Join(s.sandbox, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		// A retry rewrites a read-only file.
		_ = os.Remove(path)
		if err := os.WriteFile(path, b, 0o444); err != nil { //nolint:gosec // G306: postgres reads the init script as its own user
			return err
		}
	}
	s.relays = p.Relays
	s.run.InnerSubnet, s.run.OuterSubnet = inner.String(), outer.String()
	s.lr.mu.Lock()
	s.lr.run.InnerSubnet, s.lr.run.OuterSubnet = s.run.InnerSubnet, s.run.OuterSubnet
	s.lr.mu.Unlock()
	return nil
}

// debugger readies a debug run's adapter tools on the host (the pinned
// Delve build) before anything starts.
func (s *starter) debugger(ctx context.Context) error {
	s.lr.mu.Lock()
	a := s.lr.debug
	s.lr.mu.Unlock()
	if a == nil {
		return nil
	}
	spec, err := a.Spec(ctx, s.f)
	if err != nil {
		return &stepError{step: "debugger", err: err}
	}
	s.debug = spec
	return nil
}

// services starts each service in scenario order and waits for its ready
// probe.
func (s *starter) services(ctx context.Context) error {
	wait := strconv.Itoa(int(ReadyTimeout.Seconds()))
	for _, name := range s.sc.Services {
		if err := s.must(ctx, "service "+name, name, nil, "up", "-d", "--wait", "--wait-timeout", wait, name); err != nil {
			return err
		}
	}
	return nil
}

// fixtures loads SQL fixtures through psql in their service and runs script
// fixtures in the app container.
func (s *starter) fixtures(ctx context.Context) error {
	for i, fx := range s.sc.Fixtures {
		step := fmt.Sprintf("fixture %d", i+1)
		switch {
		case fx.SQL != "":
			if err := s.sqlFixture(ctx, step, fx); err != nil {
				return err
			}
		case fx.Script != "":
			if err := s.must(ctx, step, "", nil, "run", "--rm", ServiceApp, "sh", "-c", shellx.Quote("./"+fx.Script)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *starter) sqlFixture(ctx context.Context, step string, fx Fixture) error {
	svc := s.f.Services[fx.Into]
	if svc.Database == nil || svc.Database.Kind != "postgres" {
		return &stepError{step: step, err: fmt.Errorf("sql fixture into %s, which is not a postgres database", fx.Into)}
	}
	user := svc.Env["POSTGRES_USER"]
	if user == "" {
		user = "postgres"
	}
	db := svc.Database.Name
	if db == "" {
		db = svc.Env["POSTGRES_DB"]
	}
	if db == "" {
		db = user
	}
	f, err := os.Open(filepath.Join(s.dir, "src", filepath.Clean(fx.SQL))) //nolint:gosec // G304: a validated repository path inside the exported tree
	if err != nil {
		return &stepError{step: step, err: err}
	}
	defer f.Close()
	return s.must(ctx, step, fx.Into, f, "exec", "-T", fx.Into, "psql", "-v", "ON_ERROR_STOP=1", "-U", user, "-d", db)
}

func (s *starter) setup(ctx context.Context) error {
	for i, sc := range s.sc.Setup {
		if err := s.must(ctx, fmt.Sprintf("setup %d", i+1), "", nil, "run", "--rm", ServiceApp, "sh", "-c", sc.Run); err != nil {
			return err
		}
	}
	return nil
}

func (s *starter) build(ctx context.Context) error {
	if s.f.App.Build == "" {
		return nil
	}
	return s.must(ctx, "build", "", nil, "run", "--rm", ServiceApp, "sh", "-c", s.f.App.Build)
}

func (s *starter) app(ctx context.Context) error {
	if err := s.probe(ctx); err != nil {
		return err
	}
	if s.debug != nil && s.debug.Adapter != nil {
		if err := s.must(ctx, "debug adapter", "", nil, "up", "-d", ServiceAdapter); err != nil {
			return err
		}
	}
	return s.must(ctx, "app", "", nil, "up", "-d", ServiceApp)
}

// probe runs a debug run's probe in a one-off app container and refuses the
// run when its check does.
func (s *starter) probe(ctx context.Context) error {
	if s.debug == nil || s.debug.Probe == nil {
		return nil
	}
	p := s.debug.Probe
	const step = "debug probe"
	args := append([]string{"run", "--rm", "--no-deps", ServiceApp}, p.Command...)
	out, exit, err := s.compose(ctx, step, nil, args...)
	if err != nil || exit != 0 {
		return &stepError{step: step, exit: exit, out: out, err: err}
	}
	if err := p.Check(out); err != nil {
		return &stepError{step: step, err: err}
	}
	return nil
}

// placeLaunch records the debug run's DAP request for the debug subnet of
// slot n; the subnet, and so the app's address, is known only here.
func (s *starter) placeLaunch(n int) error {
	s.lr.mu.Lock()
	a := s.lr.debug
	s.lr.mu.Unlock()
	if a == nil {
		return nil
	}
	l, err := a.Launch(s.f, DebugNetOf(debugSubnet(n)), s.debug)
	if err != nil {
		return err
	}
	s.run.DebugLaunch = &l
	s.lr.mu.Lock()
	s.lr.run.DebugLaunch = &l
	s.lr.mu.Unlock()
	return nil
}

// edge starts the edge, checks it over its control socket, and reads the
// host port Docker published for each relay.
func (s *starter) edge(ctx context.Context) error {
	if err := s.must(ctx, "edge", "", nil, "up", "-d", "--wait", ServiceEdge); err != nil {
		return err
	}
	s.lr.mu.Lock()
	token := s.lr.token
	s.lr.mu.Unlock()
	if _, err := s.m.opts.Edge.Health(ctx, filepath.Join(s.dir, "control", ControlSocketName), token); err != nil {
		return &stepError{step: "edge", err: err}
	}
	ports := make([]RunPort, 0, len(s.relays))
	for _, r := range s.relays {
		out, exit, err := s.compose(ctx, "port "+r.Name, nil, "port", ServiceEdge, strconv.Itoa(r.Port))
		if err != nil || exit != 0 {
			return &stepError{step: "port " + r.Name, exit: exit, out: out, err: err}
		}
		ap, perr := netip.ParseAddrPort(strings.TrimSpace(firstLine(out)))
		if perr != nil {
			return &stepError{step: "port " + r.Name, err: fmt.Errorf("read published port %q: %w", out, perr)}
		}
		ports = append(ports, RunPort{Name: r.Name, Service: r.Service, HostPort: int(ap.Port())})
	}
	s.run.Ports = ports
	return nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// readyRun marks the run ready unless a Down got to it first.
func (m *Manager) readyRun(lr *liveRun, ports []RunPort) {
	names := make([]string, 0, len(ports))
	for _, p := range ports {
		names = append(names, p.Name)
	}
	lr.mu.Lock()
	if lr.stopping {
		lr.mu.Unlock()
		return
	}
	// The ledger row goes in before the state turns ready: a caller that
	// waits for ready may call harness.check at once, and its row must
	// follow this one.
	m.record(lr.run.Session, domain.EventActorAiman, domain.EventHarnessReady, map[string]any{"run": lr.run.ID, "ports": names})
	now := m.opts.Now()
	lr.run.State, lr.run.Ports, lr.run.ReadyAt, lr.run.LastUsed = StateReady, ports, &now, now
	run := lr.run
	lr.mu.Unlock()
	if err := m.writeRun(run); err != nil {
		m.logf("write run %s: %v", run.ID, err)
	}
}

// failRun takes a failed start down and records the step, unless a Down got
// to it first.
func (m *Manager) failRun(lr *liveRun, err error) {
	lr.mu.Lock()
	if lr.stopping {
		lr.mu.Unlock()
		return
	}
	lr.stopping = true
	lr.mu.Unlock()
	se, ok := err.(*stepError)
	if !ok {
		se = &stepError{step: "start", err: err}
	}
	msg := tailLines(se.out, errorTailLines)
	if se.err != nil {
		msg = strings.TrimSpace(se.err.Error() + "\n" + msg)
	}
	run := lr.snapshot()
	m.composeDown(context.Background(), run)
	now := m.opts.Now()
	run.State, run.FailedStep, run.FailedService, run.Error, run.StoppedAt = StateFailed, se.step, se.service, msg, &now
	var he *Error
	if errors.As(se.err, &he) {
		run.ErrorCode = he.Code
	}
	if err := m.writeRun(run); err != nil {
		m.logf("write run %s: %v", run.ID, err)
	}
	m.record(run.Session, domain.EventActorAiman, domain.EventHarnessFailed, map[string]any{"run": run.ID, "step": se.step, "exit": se.exit})
	lr.mu.Lock()
	lr.run = run
	lr.mu.Unlock()
	m.release(lr)
	m.prune(run.Session)
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
