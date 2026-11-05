//go:build !windows

package limits

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	pty "github.com/creack/pty/v2"

	"github.com/bouwerp/aiman/internal/ptyruntime"
)

const (
	probeCols = 120
	probeRows = 40

	probePoll = 100 * time.Millisecond
	// probeSettle is how long a screen must stay unchanged before it is believed.
	// A dialog drawn just after the composer must not lose the race with a key,
	// and a half-drawn panel must not be read.
	probeSettle = 400 * time.Millisecond
	// probeNudgeDelay lets the CLI install its SIGWINCH handler after first output.
	probeNudgeDelay = 250 * time.Millisecond
	// probeNudgePause separates the two size changes: SIGWINCH does not queue, so
	// two in one burst read as no change at all (internal/ptyhold/holder.go).
	probeNudgePause = 80 * time.Millisecond
	// probeTypePause separates /usage from its Enter. A TUI can read a single
	// burst as a paste, which leaves the text unsubmitted.
	probeTypePause = 250 * time.Millisecond

	probeWaitLimit = 5 * time.Second
	// probeOutputLimit bounds what is kept and re-rendered in one run.
	probeOutputLimit = 8 << 20

	reasonNoStart  = "could not start the CLI"
	reasonNoScreen = "the CLI showed no usable screen before the time limit"
	reasonNoPanel  = "no usage panel appeared before the time limit"
	reasonBadPanel = "could not read the usage panel"
	reasonExited   = "the CLI exited before showing usage"
	reasonOutput   = "the CLI drew too much output"
	reasonKeys     = "could not send keys to the CLI"
)

// probeEnvPassed is the part of serve's environment the CLI may see. Serve's
// environment can hold API keys, so this is a list and never a copy.
var probeEnvPassed = []string{"USER", "LOGNAME", "LANG", "COLORTERM"}

// RunGrokProbe runs the probe once and always leaves nothing running.
func RunGrokProbe(ctx context.Context, cfg ProbeConfig) Reading {
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	defer removeProbeSessions(cfg.Home, cfg.Dir)
	run, err := startProbe(cfg)
	if err != nil {
		return grokUnknown(reasonNoStart)
	}
	defer run.stop()
	return run.drive(ctx)
}

// probeRun is one started CLI and its terminal.
type probeRun struct {
	cmd     *exec.Cmd
	ptmx    *os.File
	pidFile string
	release chan struct{} // ends the goroutine holding the creating OS thread
	waited  chan struct{}
	stopper sync.Once

	mu       sync.Mutex
	output   []byte
	overflow bool
	readDone chan struct{}
}

func probeEnv(cfg ProbeConfig) []string {
	env := []string{
		"HOME=" + cfg.Home,
		"PATH=" + grokPATH(os.Getenv("PATH"), cfg.Home),
		"TERM=xterm-256color",
	}
	for _, key := range probeEnvPassed {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return env
}

func startProbe(cfg ProbeConfig) (*probeRun, error) {
	if len(cfg.Command) == 0 {
		return nil, errors.New("probe: empty command")
	}
	for _, dir := range []string{cfg.Dir, filepath.Dir(cfg.PIDFile)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("probe: create %s: %w", dir, err)
		}
	}
	run, err := launchProbe(cfg)
	if errors.Is(err, syscall.EPERM) {
		run, err = launchProbe(cfg)
	}
	if err != nil {
		return nil, err
	}
	cmd := run.cmd
	go run.read()
	go func() {
		defer close(run.waited)
		_ = cmd.Wait()
	}()
	if err := os.WriteFile(cfg.PIDFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		run.stop()
		return nil, fmt.Errorf("probe: record pid: %w", err)
	}
	return run, nil
}

// launchProbe starts the CLI once, on a fresh terminal. Under heavy process churn
// the start occasionally fails with EPERM, about once in 800 starts when tests
// ran in parallel. The step that refuses is not known, and a second attempt is
// why it does not cost a reading.
func launchProbe(cfg ProbeConfig) (*probeRun, error) {
	//nolint:gosec // G204: the command is the agent binary serve resolved itself
	cmd := exec.Command(cfg.Command[0], cfg.Command[1:]...)
	cmd.Dir = cfg.Dir
	cmd.Env = probeEnv(cfg)
	run := &probeRun{
		cmd:      cmd,
		pidFile:  cfg.PIDFile,
		release:  make(chan struct{}),
		waited:   make(chan struct{}),
		readDone: make(chan struct{}),
	}
	if err := run.startLocked(); err != nil {
		return nil, err
	}
	return run, nil
}

// startLocked starts the CLI from a goroutine locked to its OS thread. Linux ties
// Pdeathsig to the creating thread, so the thread has to outlive the CLI.
func (p *probeRun) startLocked() error {
	started := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		attrs := &syscall.SysProcAttr{Setsid: true, Setctty: true}
		setProbeDeathSignal(attrs)
		ptmx, err := pty.StartWithAttrs(p.cmd, &pty.Winsize{Cols: probeCols, Rows: probeRows}, attrs)
		if err != nil {
			started <- fmt.Errorf("probe: start: %w", err)
			return
		}
		p.ptmx = ptmx
		started <- nil
		<-p.release
	}()
	return <-started
}

// read keeps the output until the terminal closes. Past the limit it only drains.
func (p *probeRun) read() {
	defer close(p.readDone)
	buf := make([]byte, 16<<10)
	for {
		n, err := p.ptmx.Read(buf)
		if n > 0 {
			p.mu.Lock()
			if len(p.output)+n > probeOutputLimit {
				p.overflow = true
			} else {
				p.output = append(p.output, buf[:n]...)
			}
			p.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// stop kills the process group, waits, and closes the terminal. The leader is
// reaped only here, so its PID, and with it the group ID, cannot be reused
// before the kill.
func (p *probeRun) stop() {
	p.stopper.Do(func() {
		defer close(p.release)
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		waitBounded(p.waited)
		_ = p.ptmx.Close()
		waitBounded(p.readDone)
		_ = os.Remove(p.pidFile)
	})
}

func waitBounded(done <-chan struct{}) {
	select {
	case <-done:
	case <-time.After(probeWaitLimit):
	}
}

func (p *probeRun) snapshot() (output []byte, overflow bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.output[:len(p.output):len(p.output)], p.overflow
}

func (p *probeRun) closed() bool {
	select {
	case <-p.readDone:
		return true
	default:
		return false
	}
}

func (p *probeRun) nudge() {
	_ = pty.Setsize(p.ptmx, &pty.Winsize{Cols: probeCols - 1, Rows: probeRows})
	time.Sleep(probeNudgePause)
	_ = pty.Setsize(p.ptmx, &pty.Winsize{Cols: probeCols, Rows: probeRows})
}

// probeState is what one run has done so far.
type probeState struct {
	firstOutput time.Time
	nudged      bool
	screen      string
	rendered    int
	changedAt   time.Time
	trustSent   bool
	usageSent   bool
	badPanel    bool
}

// refresh renders new output and records when the screen last changed.
func (p *probeRun) refresh(st *probeState, now time.Time) {
	output, _ := p.snapshot()
	if len(output) == st.rendered {
		return
	}
	if st.firstOutput.IsZero() {
		st.firstOutput = now
	}
	st.rendered = len(output)
	if screen := ptyruntime.RenderScreen(output, probeCols, probeRows); screen != st.screen {
		st.screen = screen
		st.changedAt = now
	}
}

// drive polls the screen and sends each key only on the screen that allows it.
func (p *probeRun) drive(ctx context.Context) Reading {
	st := &probeState{}
	tick := time.NewTicker(probePoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return grokUnknown(st.timeoutReason())
		case <-tick.C:
		}
		now := time.Now()
		p.refresh(st, now)
		if _, overflow := p.snapshot(); overflow {
			return grokUnknown(reasonOutput)
		}
		if st.firstOutput.IsZero() {
			if p.closed() {
				return grokUnknown(reasonExited)
			}
			continue
		}
		if !st.nudged && now.Sub(st.firstOutput) >= probeNudgeDelay && !p.closed() {
			p.nudge()
			st.nudged = true
			continue
		}
		if !st.nudged && !p.closed() || now.Sub(st.changedAt) < probeSettle {
			continue
		}
		if r, done := p.act(ctx, st); done {
			return r
		}
		if p.closed() {
			return grokUnknown(reasonExited)
		}
	}
}

func (st *probeState) timeoutReason() string {
	switch {
	case st.badPanel:
		return reasonBadPanel
	case st.usageSent:
		return reasonNoPanel
	default:
		return reasonNoScreen
	}
}

// act handles one settled screen. Trust gets one Enter, an empty composer gets
// /usage and one Enter, and every other screen gets no keys at all.
func (p *probeRun) act(ctx context.Context, st *probeState) (Reading, bool) {
	switch ClassifyGrokScreen(st.screen) {
	case GrokUsage:
		if r, ok := ParseGrokUsage(st.screen); ok {
			r.ObservedAt = time.Now()
			return r, true
		}
		st.badPanel = true
	case GrokTrust:
		if !st.trustSent && !st.usageSent {
			st.trustSent = true
			if _, err := p.ptmx.WriteString("\r"); err != nil {
				return grokUnknown(reasonKeys), true
			}
		}
	case GrokComposer:
		if !st.usageSent {
			st.usageSent = true
			if err := p.sendUsage(ctx, st); err != nil {
				return grokUnknown(reasonKeys), true
			}
		}
	case GrokOther:
	}
	return Reading{}, false
}

// sendUsage types the command, then checks the screen again before Enter: if a
// dialog came up meanwhile, Enter would answer it.
func (p *probeRun) sendUsage(ctx context.Context, st *probeState) error {
	if _, err := p.ptmx.WriteString(grokUsageCommand); err != nil {
		return fmt.Errorf("probe: type command: %w", err)
	}
	select {
	case <-ctx.Done():
		return nil
	case <-time.After(probeTypePause):
	}
	p.refresh(st, time.Now())
	if !grokReadyForEnter(st.screen) {
		return nil
	}
	if _, err := p.ptmx.WriteString("\r"); err != nil {
		return fmt.Errorf("probe: submit command: %w", err)
	}
	return nil
}

// removeProbeSessions removes the folder the CLI keeps for the probe's working
// directory. Only an exact match on the decoded name goes: another session could
// start at the same moment, so nothing is chosen by age or by watching the tree.
func removeProbeSessions(home, dir string) {
	sessions := filepath.Join(home, ".grok", "sessions")
	entries, err := os.ReadDir(sessions)
	if err != nil {
		return
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		resolved = dir
	}
	for _, entry := range entries {
		decoded, err := url.PathUnescape(entry.Name())
		if err == nil && (decoded == dir || decoded == resolved) {
			_ = os.RemoveAll(filepath.Join(sessions, entry.Name()))
		}
	}
}

// ReapStaleProbe kills the PID in pidFile only if its working directory is dir.
// A reused PID belongs to some other folder and is left alone.
func ReapStaleProbe(pidFile, dir string) {
	raw, err := os.ReadFile(pidFile) //nolint:gosec // G304: serve's own file under ~/.aiman
	if err != nil {
		return
	}
	defer func() { _ = os.Remove(pidFile) }()
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 || pid == os.Getpid() {
		return
	}
	cwd, err := probeProcessDir(pid)
	if err != nil || !sameDir(cwd, dir) {
		return
	}
	target := pid
	if pgid, err := syscall.Getpgid(pid); err == nil && pgid == pid {
		target = -pid
	}
	_ = syscall.Kill(target, syscall.SIGKILL)
}

func sameDir(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}
