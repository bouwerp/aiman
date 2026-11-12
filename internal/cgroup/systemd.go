package cgroup

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// SessionsSlice holds every session scope, so one aggregate limit bounds
// them together. systemd nests it as aiman.slice/aiman-sessions.slice.
const SessionsSlice = "aiman-sessions.slice"

// fsRoot is where cgroup v2 is mounted.
const fsRoot = "/sys/fs/cgroup"

// ErrUnavailable is why Probe refuses: the host has no usable user systemd.
var ErrUnavailable = errors.New("cgroup: no user systemd on this host")

var unitUnsafe = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// ScopeName is the transient scope unit of the session id. Every character
// systemd does not take in a unit name becomes "_".
func ScopeName(id string) string {
	clean := unitUnsafe.ReplaceAllString(id, "_")
	if len(clean) > 200 {
		clean = clean[:200]
	}
	return "aiman-session-" + clean + ".scope"
}

// Runner runs one command and returns its combined output. Tests replace it.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // G204: fixed systemd binaries with unit names built by ScopeName
}

// Systemd starts and adjusts session scopes through the user manager.
type Systemd struct {
	// Root is the user manager's cgroup directory, e.g.
	// /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service.
	Root string
	run  Runner
}

// Probe returns a Systemd when this is Linux, systemd-run is on PATH and the
// user manager answers. It sets XDG_RUNTIME_DIR for this process when it is
// missing (a serve started under nohup over SSH has none), because
// systemd-run and systemctl --user find the bus through it.
func Probe(ctx context.Context) (*Systemd, error) {
	return probe(ctx, runtime.GOOS, exec.LookPath, execRunner)
}

func probe(ctx context.Context, goos string, lookPath func(string) (string, error), run Runner) (*Systemd, error) {
	if goos != "linux" {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, goos)
	}
	for _, bin := range []string{"systemd-run", "systemctl"} {
		if _, err := lookPath(bin); err != nil {
			return nil, fmt.Errorf("%w: %s not on PATH", ErrUnavailable, bin)
		}
	}
	uid := os.Getuid()
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		dir := "/run/user/" + strconv.Itoa(uid)
		if _, err := os.Stat(dir); err != nil {
			return nil, fmt.Errorf("%w: no XDG_RUNTIME_DIR and no %s", ErrUnavailable, dir)
		}
		_ = os.Setenv("XDG_RUNTIME_DIR", dir)
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if out, err := run(cctx, "systemctl", "--user", "show-environment"); err != nil {
		return nil, fmt.Errorf("%w: systemctl --user: %s", ErrUnavailable, firstLine(out, err))
	}
	own, _ := os.ReadFile("/proc/self/cgroup")
	return &Systemd{Root: managerRoot(string(own), uid), run: run}, nil
}

// NewSystemd builds a Systemd over an explicit cgroup root and runner (tests).
func NewSystemd(root string, run Runner) *Systemd { return &Systemd{Root: root, run: run} }

// managerRoot is the user manager's cgroup directory: the caller's own
// cgroup cut at user@<uid>.service, else the conventional path (a process
// in a login session scope sits beside the manager, not under it).
func managerRoot(procSelfCgroup string, uid int) string {
	unit := "user@" + strconv.Itoa(uid) + ".service"
	for _, line := range strings.Split(procSelfCgroup, "\n") {
		path, ok := strings.CutPrefix(line, "0::")
		if !ok {
			continue
		}
		if i := strings.Index(path, "/"+unit); i >= 0 {
			return filepath.Join(fsRoot, path[:i], unit)
		}
	}
	return filepath.Join(fsRoot, "user.slice", "user-"+strconv.Itoa(uid)+".slice", unit)
}

// UserSliceDir is the cgroup directory of the whole user (user-<uid>.slice),
// whose limit every session, serve and the gateway share.
func (s *Systemd) UserSliceDir() string { return filepath.Dir(s.Root) }

// SliceDir is the cgroup directory of the sessions slice.
func (s *Systemd) SliceDir() string {
	return filepath.Join(s.Root, "aiman.slice", SessionsSlice)
}

// ScopeDir is the cgroup directory of the session id's scope, whether or not
// the scope exists.
func (s *Systemd) ScopeDir(id string) string {
	return filepath.Join(s.SliceDir(), ScopeName(id))
}

// Active reports whether the session id's scope exists now. A scope lives as
// long as any process in it, so it can outlast the holder that started it.
func (s *Systemd) Active(id string) bool {
	_, err := os.Stat(filepath.Join(s.ScopeDir(id), "cgroup.procs"))
	return err == nil
}

// RunArgv is the command prefix that starts what follows it in a new scope
// for the session id. systemd-run registers the scope and then execs the
// command in place, so the command keeps the caller's pid, session and
// stdio. --collect drops a scope that ended failed (an OOM kill), so the
// name is free for the session's next terminal.
func RunArgv(id string, l Limits) []string {
	argv := []string{
		"systemd-run", "--user", "--scope", "--quiet", "--collect",
		"--unit=" + ScopeName(id), "--slice=" + SessionsSlice,
		"--description=aiman session " + id,
	}
	for _, p := range l.Properties() {
		argv = append(argv, "-p", p)
	}
	return append(argv, "--")
}

// Launch says how to put a new holder in the session id's scope: the
// command prefix that starts it in a fresh scope, or, when the scope still
// exists (a restart whose old processes linger, or a second terminal of the
// same session), the directory the holder joins, with the limits applied to
// the running scope. It implements ptyruntime.Scoper.
func (s *Systemd) Launch(id string, l Limits) (prefix []string, join string) {
	if !s.Active(id) {
		return RunArgv(id, l), ""
	}
	if err := s.SetLimits(context.Background(), id, l); err != nil {
		log.Printf("cgroup: %v", err)
	}
	return nil, s.ScopeDir(id)
}

// SetLimits applies l to the session id's running scope. The change is not
// persisted: a scope is transient, and its next start passes the limits anew.
func (s *Systemd) SetLimits(ctx context.Context, id string, l Limits) error {
	props := l.Properties()
	if len(props) == 0 {
		return nil
	}
	args := append([]string{"--user", "set-property", "--runtime", ScopeName(id)}, props...)
	return s.systemctl(ctx, args...)
}

// EnsureSlice sets the aggregate memory limit of the sessions slice, live
// and for later starts of the user manager. A non-positive value lifts it.
func (s *Systemd) EnsureSlice(ctx context.Context, memoryHigh int64) error {
	if memoryHigh <= 0 {
		memoryHigh = Infinity
	}
	return s.systemctl(ctx, "--user", "set-property", SessionsSlice, "MemoryHigh="+sizeProperty(memoryHigh))
}

func (s *Systemd) systemctl(ctx context.Context, args ...string) error {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if out, err := s.run(cctx, "systemctl", args...); err != nil {
		return fmt.Errorf("systemctl %s: %s", strings.Join(args[1:], " "), firstLine(out, err))
	}
	return nil
}

// Join moves the calling process into the cgroup directory dir. A holder
// calls it before it starts its command, so the command and everything it
// runs are charged to that scope.
func Join(dir string) error {
	if dir == "" {
		return nil
	}
	return os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(strconv.Itoa(os.Getpid())), 0o644) //nolint:gosec // G306: a cgroup control file, mode is ignored
}

// DefaultSliceMemoryHigh derives the sessions slice's aggregate limit from
// the user slice's own soft limit: that limit less headroom for serve, the
// gateway and whatever else the user runs (a sixth of it, at least 2G). It
// is 0, no aggregate limit, when the user slice has none.
func DefaultSliceMemoryHigh(userSliceHigh int64) int64 {
	if userSliceHigh <= 0 {
		return 0
	}
	headroom := max(userSliceHigh/6, 2<<30)
	if headroom >= userSliceHigh {
		return userSliceHigh / 2
	}
	return userSliceHigh - headroom
}

func firstLine(out []byte, err error) string {
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if line == "" && err != nil {
		return err.Error()
	}
	return line
}
