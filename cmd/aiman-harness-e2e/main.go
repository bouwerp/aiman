//go:build e2e

// Command aiman-harness-e2e runs one harness web console against real Docker
// runs on this host, for browser end-to-end tests. It starts its own serve
// in process (temp socket, temp sqlite database, its own harness directory,
// no image vetting), registers one session whose worktree is the fixture
// repository, and serves the console on 127.0.0.1 with forwards that are the
// run's own host ports. Several drivers can run at once: each has its own
// directory, socket, database, session and console port.
//
// Build and run from the repository root (the edge binary is built from it):
//
//	go run -tags e2e ./cmd/aiman-harness-e2e --fixture go --up
//
// It prints E2E_* lines on stdout and keeps running until SIGINT or SIGTERM,
// then takes down every run of its session.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bouwerp/aiman/internal/console"
	"github.com/bouwerp/aiman/internal/contextstore"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/harnesstest"
	"github.com/bouwerp/aiman/internal/harness/tools"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
	"github.com/bouwerp/aiman/internal/review"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/todo"
	"github.com/bouwerp/aiman/internal/usecase"
)

// defaultRelease is the aiman release whose bun-dap asset the Bun adapter
// downloads when the tools cache has none.
const defaultRelease = "v0.60.0"

// plainAppImage runs an app whose repository names no language.
const plainAppImage = "busybox:1.36"

// allowedImages is the pre-approval list with vetting off: every image a
// fixture uses, the debug adapter's included.
var allowedImages = []string{
	"busybox:*", "postgres:16", "golang:*", "node:*", "python:*", "oven/bun:*",
	anvilImage, bun12Image, harness.AdapterImage,
}

type flags struct {
	repo, fixture, scenario, network, urlFile, release, aimanBin, toolsDir string
	port, maxRuns                                                          int
	debug, debugChecks, up, keep                                           bool
	attachTimeout                                                          time.Duration
}

func parseFlags() flags {
	var f flags
	flag.StringVar(&f.repo, "repo", "", "fixture repository: a git repo with a committed .aiman/harness.yaml")
	flag.StringVar(&f.fixture, "fixture", "", "materialise a built-in fixture: "+strings.Join(fixtureNames(), "|"))
	flag.StringVar(&f.scenario, "scenario", "", "scenario id for --up (default: the fixture's)")
	flag.StringVar(&f.network, "network", harness.NetworkOffline, "online|offline")
	flag.IntVar(&f.port, "port", 0, "console port on 127.0.0.1 (0: the kernel picks)")
	flag.IntVar(&f.maxRuns, "max-runs", 2, "live runs this driver allows")
	flag.BoolVar(&f.debug, "debug", false, "with --up: start a debug run")
	flag.BoolVar(&f.debugChecks, "debug-checks", false, "with --up: start a run that debugs only its checks")
	flag.BoolVar(&f.up, "up", false, "start the run (else the page's Up button does)")
	flag.BoolVar(&f.keep, "keep", false, "keep the driver directory on exit")
	flag.StringVar(&f.urlFile, "url-file", "", "also write the console URL to this file")
	flag.StringVar(&f.release, "release", defaultRelease, "aiman release the Bun adapter takes bun-dap from")
	flag.StringVar(&f.aimanBin, "aiman-bin", "", "static aiman binary for the edge (default: build ./cmd/aiman without cgo)")
	flag.DurationVar(&f.attachTimeout, "attach-timeout", 0, "harness.debug.attach_timeout: how long a debug check waits for a debugger (0: the default, 10m)")
	flag.StringVar(&f.toolsDir, "tools-dir", "", "debug tools store (default: the Docker tests' cache, shared)")
	flag.Parse()
	return f
}

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.SetPrefix("e2e: ")
	if err := run(parseFlags()); err != nil {
		log.Printf("%v", err)
		os.Exit(1)
	}
}

func run(f flags) error {
	if (f.repo == "") == (f.fixture == "") {
		return errors.New("name exactly one of --repo and --fixture")
	}
	if f.network != harness.NetworkOffline && f.network != harness.NetworkOnline {
		return fmt.Errorf("--network %q: online or offline", f.network)
	}
	// The console and this process call serve as the operator; a session's
	// AIMAN_ID in the environment would make every call that session's.
	_ = os.Unsetenv("AIMAN_ID")

	// Unix sockets cap their path at 108 bytes: the serve socket and every
	// run's control socket live under this short directory.
	dir, err := os.MkdirTemp("/tmp", "aimane2e-")
	if err != nil {
		return err
	}
	if !f.keep {
		defer func() { _ = os.RemoveAll(dir) }()
	}
	fmt.Printf("E2E_DIR=%s\n", dir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	repoDir, scenario := f.repo, f.scenario
	var images []string
	if f.fixture != "" {
		fx, ok := fixtures[f.fixture]
		if !ok {
			return fmt.Errorf("--fixture %q: one of %s", f.fixture, strings.Join(fixtureNames(), ", "))
		}
		repoDir = filepath.Join(dir, "repo")
		if err := fx.write(ctx, repoDir); err != nil {
			return fmt.Errorf("fixture %s: %w", f.fixture, err)
		}
		if scenario == "" {
			scenario = fx.scenario
		}
		images = fx.images
	}
	if repoDir, err = filepath.Abs(repoDir); err != nil {
		return err
	}
	fmt.Printf("E2E_REPO=%s\n", repoDir)
	if f.up && scenario == "" {
		return errors.New("--up needs --scenario")
	}
	if err := ensureImages(ctx, append([]string{harness.EdgeImage}, images...)); err != nil {
		return err
	}

	st, err := startStack(ctx, f, dir)
	if err != nil {
		return err
	}
	defer st.close()

	session := "e2e-" + filepath.Base(dir)[len("aimane2e-"):]
	if err := st.repo.Save(ctx, &domain.Session{
		ID: session, Name: session, Status: domain.SessionStatusActive,
		WorktreePath: repoDir, WorkingDirectory: repoDir, CreatedAt: time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("register the session: %w", err)
	}
	fmt.Printf("E2E_SESSION=%s\n", session)

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(f.port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("console listener: %w", err)
	}
	rpc := socketRPC{socket: st.socket}
	cs, err := console.New(console.Options{RPC: rpc, Forwarder: localForwarder{}, Session: session, Log: os.Stderr}, ln)
	if err != nil {
		_ = ln.Close()
		return err
	}
	fmt.Printf("E2E_CONSOLE_URL=%s\n", cs.URL())
	if f.urlFile != "" {
		if err := os.WriteFile(f.urlFile, []byte(cs.URL()+"\n"), 0o600); err != nil {
			return err
		}
	}
	if f.up {
		go upAndReport(ctx, rpc, st.mgr, session, scenario, f.debug, f.debugChecks)
	}
	fmt.Println("E2E_READY=1")
	err = cs.Serve(ctx)
	downAll(st.mgr, session)
	return err
}

// stack is the in-process serve with its harness manager.
type stack struct {
	socket string
	repo   *sqlite.Repository
	mgr    *harness.Manager
	cancel context.CancelFunc
	done   chan struct{}
}

func startStack(parent context.Context, f flags, dir string) (*stack, error) {
	bin := f.aimanBin
	if bin == "" {
		log.Printf("building the static aiman binary for the edge")
		var err error
		if bin, err = harnesstest.EdgeBinary(); err != nil {
			return nil, err
		}
	}
	toolsDir := f.toolsDir
	if toolsDir == "" {
		cache, err := harnesstest.CacheDir()
		if err != nil {
			return nil, err
		}
		toolsDir = filepath.Join(cache, "tools")
	}
	ln, err := server.Listen(dir)
	if err != nil {
		return nil, err
	}
	repo, err := sqlite.NewRepository(filepath.Join(dir, "aiman.db"))
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	srv := server.New(ln, repo, nil, nil, contextstore.NewFiles(filepath.Join(dir, "context")), nil, "e2e")
	srv.EnableTodos(todo.NewFiles(filepath.Join(dir, "todos")), repo)
	store := &tools.Store{Dir: toolsDir}
	m, err := harness.NewManager(harness.Options{
		DebugAdapters: []harness.DebugAdapter{
			&harness.GoDebug{Tools: &harness.DlvTools{Dir: toolsDir, Pin: harness.DefaultDlv}},
			&harness.NodeDebug{Tools: store},
			&harness.BunDebug{Tools: store, Release: f.release},
			&harness.PythonDebug{Tools: store},
		},
		Dir:      filepath.Join(dir, "h"),
		AimanBin: bin,
		UID:      os.Getuid(), GID: os.Getgid(),
		AllowedImages:      allowedImages,
		MaxRuns:            f.maxRuns,
		DebugAttachTimeout: f.attachTimeout,
		Network:            f.network,
		Docker:             harness.ExecDocker{},
		AppImage:           appImage,
		Record:             srv.HarnessRecorder(),
	})
	if err != nil {
		_ = repo.Close()
		_ = ln.Close()
		return nil, err
	}
	srv.SetHarness(server.HarnessDeps{Manager: m, Repos: map[string]config.RepoBootstrap{}})
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx)
	}()
	go m.RunIdle(ctx)
	return &stack{socket: server.SocketPath(dir), repo: repo, mgr: m, cancel: cancel, done: done}, nil
}

func (s *stack) close() {
	s.mgr.Close()
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(3 * time.Second):
	}
	_ = s.repo.Close()
}

// appImage is review.Image, falling back to busybox for a repository that
// names no language (the db, evm and plain fixtures).
func appImage(src, override string) (string, error) {
	img, err := review.Image(src, override)
	if errors.Is(err, review.ErrNoLanguage) {
		return plainAppImage, nil
	}
	return img, err
}

// downAll takes down every live run of session before the manager closes,
// which would otherwise leave their containers behind.
func downAll(m *harness.Manager, session string) {
	runs, err := m.Runs(session)
	if err != nil {
		log.Printf("list runs: %v", err)
		return
	}
	for _, r := range runs {
		if !r.Live() {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), server.HarnessDownTimeout)
		log.Printf("taking down run %s", r.ID)
		if err := m.Down(ctx, r.ID, harness.StopDown); err != nil {
			log.Printf("down %s: %v (docker compose -p harness-%s down -v)", r.ID, err, r.ID)
		}
		cancel()
	}
}

// upAndReport starts the run through serve, as the page's Up button does,
// and prints its state once it leaves starting.
func upAndReport(ctx context.Context, rpc socketRPC, m *harness.Manager, session, scenario string, debug, debugChecks bool) {
	raw, err := rpc.Call(ctx, "harness.up", map[string]any{
		"session": session, "scenario": scenario, "debug": debug, "debug_checks": debugChecks,
	})
	if err != nil {
		fmt.Printf("E2E_RUN_ERROR=%s\n", oneLine(err.Error()))
		return
	}
	var res struct {
		Run harness.Run `json:"run"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		fmt.Printf("E2E_RUN_ERROR=%s\n", oneLine(err.Error()))
		return
	}
	fmt.Printf("E2E_RUN=%s\n", res.Run.ID)
	for ctx.Err() == nil {
		r, err := m.Get(res.Run.ID)
		if err != nil {
			fmt.Printf("E2E_RUN_ERROR=%s\n", oneLine(err.Error()))
			return
		}
		if r.State != harness.StateStarting {
			if r.State == harness.StateFailed {
				fmt.Printf("E2E_RUN_STATE=failed step=%q error=%q\n", r.FailedStep, oneLine(r.Error))
			} else {
				fmt.Printf("E2E_RUN_STATE=%s\n", r.State)
			}
			return
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// ensureImages pulls each image the engine does not have: with vetting off,
// a run uses only images already on the host.
func ensureImages(ctx context.Context, refs []string) error {
	for _, ref := range refs {
		if exec.CommandContext(ctx, "docker", "image", "inspect", ref).Run() == nil { //nolint:gosec // G204: fixture images
			continue
		}
		log.Printf("pulling %s", ref)
		if out, err := exec.CommandContext(ctx, "docker", "pull", ref).CombinedOutput(); err != nil { //nolint:gosec // G204: fixture images
			return fmt.Errorf("pull %s: %w: %s", ref, err, out)
		}
	}
	return nil
}

// socketRPC calls the in-process serve as the operator: Client's zero
// Caller sends none, and serve has no pty holders to place this process in.
type socketRPC struct{ socket string }

func (r socketRPC) Call(_ context.Context, method string, params map[string]any) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	resp, err := server.Client{Socket: r.socket}.CallRaw(method, raw)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, &usecase.RPCError{Code: resp.Error.Code, Message: resp.Error.Message}
	}
	return json.Marshal(resp.Result)
}

// localForwarder needs no ssh: the run's relays already listen on this
// host's 127.0.0.1, so each forward is the host port itself.
type localForwarder struct{}

func (localForwarder) Open(_ context.Context, ports []usecase.HarnessPort) (console.Forwards, error) {
	fw := &localForwards{died: make(chan usecase.HarnessForward)}
	for _, p := range ports {
		fw.fwds = append(fw.fwds, usecase.HarnessForward{Name: p.Name, LocalPort: p.HostPort, HostPort: p.HostPort})
	}
	return fw, nil
}

type localForwards struct {
	fwds []usecase.HarnessForward
	died chan usecase.HarnessForward
}

func (l *localForwards) Forwards() []usecase.HarnessForward  { return l.fwds }
func (l *localForwards) Died() <-chan usecase.HarnessForward { return l.died }
func (l *localForwards) Close()                              {}
