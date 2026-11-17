package usecase

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Harness forward limits.
const (
	// harnessForwardReadyTimeout bounds the wait for one ssh child to listen.
	harnessForwardReadyTimeout = 20 * time.Second
	// harnessForwardAttempts is how often a relay is tried on a fresh local
	// port when its ssh child exits before it listens (another process took
	// the port between the pick and ssh's bind).
	harnessForwardAttempts = 3
	// harnessForwardStopWait is how long Stop waits after SIGTERM before it
	// kills the child.
	harnessForwardStopWait = 3 * time.Second
	// harnessForwardStderrCap bounds the ssh stderr kept for an error message.
	harnessForwardStderrCap = 4096
)

// HarnessPort is a relay of a harness run: the edge publishes it on the
// host's 127.0.0.1:HostPort.
type HarnessPort struct {
	Name     string
	HostPort int
}

// HarnessForward is one open forward from the laptop's 127.0.0.1:LocalPort to
// the host's 127.0.0.1:HostPort.
type HarnessForward struct {
	Name      string
	LocalPort int
	HostPort  int
}

// URL is the laptop URL of the forward.
func (f HarnessForward) URL() string {
	return "http://127.0.0.1:" + strconv.Itoa(f.LocalPort)
}

// ForwardProc is one running ssh forward child.
type ForwardProc interface {
	// Done is closed when the child has exited.
	Done() <-chan struct{}
	// Err is the child's exit error once Done is closed.
	Err() error
	// Stop ends the child and waits for it.
	Stop() error
}

// HarnessForwarder opens one ssh child per relay of a harness run. The
// children are held by the calling process: none backgrounds itself, so each
// forward ends with the process that opened it. The zero values of Start,
// FreePort and Ready run ssh, pick a free laptop port and wait until the
// forward listens.
type HarnessForwarder struct {
	Target   string
	Start    func(ctx context.Context, args []string) (ForwardProc, error)
	FreePort func() (int, error)
	Ready    func(ctx context.Context, port int, proc ForwardProc) error
}

// HarnessForwardArgs is the ssh argv of one forward: no command (-N), never
// -f, no agent, batch mode, its own connection (a multiplexed forward would
// live in the master after the child exits), and exit when the bind fails.
// Both ends bind 127.0.0.1.
func HarnessForwardArgs(target string, localPort, hostPort int) []string {
	return []string{
		"-N",
		"-o", "BatchMode=yes",
		"-o", "ForwardAgent=no",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		"-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", localPort, hostPort),
		target,
	}
}

// HarnessForwards is the set of forwards one Open started.
type HarnessForwards struct {
	fwds  []HarnessForward
	procs []ForwardProc
	died  chan HarnessForward
	once  sync.Once
}

// Forwards lists the open forwards in the order they were asked for.
func (h *HarnessForwards) Forwards() []HarnessForward { return h.fwds }

// Died receives each forward whose child exits, including at Close.
func (h *HarnessForwards) Died() <-chan HarnessForward { return h.died }

// Close stops every child and waits for each.
func (h *HarnessForwards) Close() {
	h.once.Do(func() {
		for _, p := range h.procs {
			_ = p.Stop()
		}
	})
}

// Open starts a forward for each port. On any failure it stops the children
// it started and returns the error.
func (f HarnessForwarder) Open(ctx context.Context, ports []HarnessPort) (*HarnessForwards, error) {
	f = f.withDefaults()
	h := &HarnessForwards{died: make(chan HarnessForward, len(ports))}
	for _, p := range ports {
		fwd, proc, err := f.openOne(ctx, p)
		if err != nil {
			h.Close()
			return nil, fmt.Errorf("forward %s (host port %d): %w", p.Name, p.HostPort, err)
		}
		h.fwds = append(h.fwds, fwd)
		h.procs = append(h.procs, proc)
		go func() {
			<-proc.Done()
			h.died <- fwd
		}()
	}
	return h, nil
}

func (f HarnessForwarder) openOne(ctx context.Context, p HarnessPort) (HarnessForward, ForwardProc, error) {
	var lastErr error
	for range harnessForwardAttempts {
		local, err := f.FreePort()
		if err != nil {
			return HarnessForward{}, nil, fmt.Errorf("pick a laptop port: %w", err)
		}
		proc, err := f.Start(ctx, HarnessForwardArgs(f.Target, local, p.HostPort))
		if err != nil {
			return HarnessForward{}, nil, fmt.Errorf("start ssh: %w", err)
		}
		err = f.Ready(ctx, local, proc)
		if err == nil {
			return HarnessForward{Name: p.Name, LocalPort: local, HostPort: p.HostPort}, proc, nil
		}
		_ = proc.Stop()
		lastErr = err
		if !exited(proc) {
			break
		}
	}
	return HarnessForward{}, nil, lastErr
}

func exited(p ForwardProc) bool {
	select {
	case <-p.Done():
		return true
	default:
		return false
	}
}

func (f HarnessForwarder) withDefaults() HarnessForwarder {
	if f.Start == nil {
		f.Start = startSSHForward
	}
	if f.FreePort == nil {
		f.FreePort = freeLoopbackPort
	}
	if f.Ready == nil {
		f.Ready = waitForwardListening
	}
	return f
}

// freeLoopbackPort asks the kernel for a free port on 127.0.0.1. ssh binds it
// a moment later; ExitOnForwardFailure turns a lost race into an exit.
func freeLoopbackPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	return port, ln.Close()
}

// waitForwardListening polls the laptop port until ssh accepts on it, the
// child exits, or the wait ends.
func waitForwardListening(ctx context.Context, port int, proc ForwardProc) error {
	ctx, cancel := context.WithTimeout(ctx, harnessForwardReadyTimeout)
	defer cancel()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	var d net.Dialer
	for {
		select {
		case <-proc.Done():
			return fmt.Errorf("ssh exited before the forward listened: %w", proc.Err())
		case <-ctx.Done():
			return fmt.Errorf("ssh did not listen on %s: %w", addr, ctx.Err())
		case <-tick.C:
		}
		c, err := d.DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = c.Close()
			return nil
		}
	}
}

// sshForward is a real ssh child.
type sshForward struct {
	cmd    *exec.Cmd
	done   chan struct{}
	err    error
	stderr *cappedBuffer
}

// startSSHForward runs ssh in the foreground of this process: it is not
// detached and gets no stdin.
func startSSHForward(_ context.Context, args []string) (ForwardProc, error) {
	cmd := exec.Command("ssh", args...) //nolint:gosec // G204: fixed binary; argv built by HarnessForwardArgs
	buf := &cappedBuffer{max: harnessForwardStderrCap}
	cmd.Stderr = buf
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &sshForward{cmd: cmd, done: make(chan struct{}), stderr: buf}
	go func() {
		err := cmd.Wait()
		if err != nil {
			if msg := strings.TrimSpace(buf.String()); msg != "" {
				err = fmt.Errorf("%w: %s", err, msg)
			}
		}
		p.err = err
		close(p.done)
	}()
	return p, nil
}

func (p *sshForward) Done() <-chan struct{} { return p.done }

func (p *sshForward) Err() error {
	<-p.done
	return p.err
}

func (p *sshForward) Stop() error {
	select {
	case <-p.done:
		return nil
	default:
	}
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		_ = p.cmd.Process.Kill()
	}
	select {
	case <-p.done:
	case <-time.After(harnessForwardStopWait):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
	return nil
}

// cappedBuffer keeps the first max bytes written to it.
type cappedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.max - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
