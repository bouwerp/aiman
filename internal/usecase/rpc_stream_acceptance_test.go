//go:build pileup_c

package usecase

// Contract (docs/specs/client-pileup-guard.md, Behaviour P1, laptop side):
//
//   - type RPCStreamProcess interface {
//     Stdin() io.WriteCloser
//     Stdout() io.Reader
//     Wait() int   // blocks until exit; the exit status, -1 when killed
//     Kill() error
//     }
//     One started `ssh … exec aiman rpc --stream`.
//
//   - type RPCStreamRemote interface {
//     TerminalExecutor
//     RPCStreamKey() string // calls with the same key share one stream
//     }
//     ssh.Manager implements it (its target), plus
//     RPCStreamCommand() *exec.Cmd, the dedicated ControlMaster=no command the
//     default starter wraps.
//
//   - type RPCStreams struct {
//     Now   func() time.Time                                   // nil is time.Now
//     Start func(remote RPCStreamRemote) (RPCStreamProcess, error) // nil wraps RPCStreamCommand
//     }
//     func NewRPCStreams() *RPCStreams
//     func (s *RPCStreams) Call(ctx context.Context, remote TerminalExecutor, method string, params any) (json.RawMessage, error)
//     Call answers like ServeRPC. A remote that is not an RPCStreamRemote is
//     called by exec exactly as today. ServeRPC is Call on a package-level
//     RPCStreams.
//
//   - var ErrServeUnreachable error: a call while the remote's stream is down
//     (cooling down after a stall, or backing off after a death that was not an
//     old binary) fails fast with it and never execs.
//
// Behaviour pinned here: one stream per key, started lazily on a call; a call
// writes `{"id","method","params"}` and waits for its id under ctx; answers
// match by id and a late one is dropped. A down stream is started again only by
// a call made at or after the end of its cooldown or backoff, judged by Now.
// A timeout while the stream has written no line since the call was sent kills
// the process and holds the remote down for 60s. A remote command that exits
// with a status other than 255 before the `{"stream":1}` hello marks the remote
// as having no stream: that call and later ones use exec. No hello by the
// call's deadline while the process runs is a stall, never "no stream".

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// rsClock is a settable clock.
type rsClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *rsClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *rsClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// rsRequest is one line the laptop wrote to the stream.
type rsRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// rsProc is a fake remote process on pipes.
type rsProc struct {
	inR    *io.PipeReader
	inW    *io.PipeWriter
	outR   *io.PipeReader
	outW   *io.PipeWriter
	outMu  sync.Mutex
	done   chan struct{}
	once   sync.Once
	code   int
	killed atomic.Bool
	reqs   chan rsRequest
}

func newRSProc() *rsProc {
	p := &rsProc{done: make(chan struct{}), reqs: make(chan rsRequest, 128)}
	p.inR, p.inW = io.Pipe()
	p.outR, p.outW = io.Pipe()
	go func() {
		sc := bufio.NewScanner(p.inR)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for sc.Scan() {
			var r rsRequest
			if json.Unmarshal(sc.Bytes(), &r) == nil {
				p.reqs <- r
			}
		}
	}()
	return p
}

func (p *rsProc) Stdin() io.WriteCloser { return p.inW }
func (p *rsProc) Stdout() io.Reader     { return p.outR }

func (p *rsProc) Wait() int {
	<-p.done
	return p.code
}

func (p *rsProc) Kill() error {
	p.killed.Store(true)
	p.exit(-1)
	return nil
}

func (p *rsProc) exit(code int) {
	p.once.Do(func() {
		p.code = code
		_ = p.outW.Close()
		_ = p.inR.Close()
		close(p.done)
	})
}

// write sends one stdout line in the background; the laptop reads stdout
// continuously, and a write after exit fails harmlessly.
func (p *rsProc) write(line string) {
	go func() {
		p.outMu.Lock()
		defer p.outMu.Unlock()
		_, _ = p.outW.Write([]byte(line + "\n"))
	}()
}

func (p *rsProc) hello() { p.write(`{"stream":1}`) }

func (p *rsProc) answer(r rsRequest) {
	p.write(fmt.Sprintf(`{"id":%s,"result":{"via":"stream","params":%s}}`, r.ID, r.Params))
}

// serveAll answers every request, forever.
func (p *rsProc) serveAll() {
	go func() {
		for r := range p.reqs {
			p.answer(r)
		}
	}()
}

// rsRemote is a fake remote: a stream key plus an exec path that counts.
type rsRemote struct {
	key   string
	execN atomic.Int32
	mu    sync.Mutex
	cmds  []string
}

func (r *rsRemote) Execute(_ context.Context, cmd string) (string, error) {
	r.execN.Add(1)
	r.mu.Lock()
	r.cmds = append(r.cmds, cmd)
	r.mu.Unlock()
	return `{"result":{"via":"exec"}}`, nil
}

func (r *rsRemote) WriteFile(context.Context, string, []byte) error { return nil }
func (r *rsRemote) RPCStreamKey() string                            { return r.key }

// rsRig wires RPCStreams to a fake clock and a queue of fake processes.
type rsRig struct {
	streams *RPCStreams
	clock   *rsClock
	remote  *rsRemote
	mu      sync.Mutex
	procs   []*rsProc
	next    []func(p *rsProc)
}

func newRSRig(t *testing.T, scripts ...func(p *rsProc)) *rsRig {
	t.Helper()
	g := &rsRig{
		clock:  &rsClock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)},
		remote: &rsRemote{key: "code@regent0"},
		next:   scripts,
	}
	g.streams = NewRPCStreams()
	g.streams.Now = g.clock.Now
	g.streams.Start = func(remote RPCStreamRemote) (RPCStreamProcess, error) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if len(g.next) == 0 {
			t.Errorf("unexpected stream start #%d for %s", len(g.procs)+1, remote.RPCStreamKey())
			return nil, errors.New("no more fake processes")
		}
		p := newRSProc()
		script := g.next[0]
		g.next = g.next[1:]
		g.procs = append(g.procs, p)
		script(p)
		return p, nil
	}
	t.Cleanup(func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		for _, p := range g.procs {
			p.exit(0)
		}
	})
	return g
}

func (g *rsRig) starts() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.procs)
}

func (g *rsRig) proc(i int) *rsProc {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.procs[i]
}

func (g *rsRig) call(timeout time.Duration, n string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return g.streams.Call(ctx, g.remote, "todo.get", map[string]any{"n": n})
}

// healthy answers every request after the hello.
func healthy(p *rsProc) {
	p.hello()
	p.serveAll()
}

func wantVia(t *testing.T, got json.RawMessage, err error, via string) {
	t.Helper()
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if !strings.Contains(string(got), `"via":"`+via+`"`) {
		t.Fatalf("result %s did not come via %s", got, via)
	}
}

// wantUnreachable runs a call that must fail fast with ErrServeUnreachable.
func (g *rsRig) wantUnreachable(t *testing.T) {
	t.Helper()
	start := time.Now()
	_, err := g.call(5*time.Second, "down")
	if !errors.Is(err, ErrServeUnreachable) {
		t.Fatalf("call while down = %v, want ErrServeUnreachable", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("call while down took %v, want a fast failure", d)
	}
}

func TestAcceptanceRPCStreamFiftyCallsOneProcess(t *testing.T) {
	g := newRSRig(t, healthy)
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			n := fmt.Sprintf("c%d", i)
			got, err := g.call(5*time.Second, n)
			if err != nil {
				errs <- fmt.Errorf("call %s: %w", n, err)
				return
			}
			if !strings.Contains(string(got), `"n":"`+n+`"`) || !strings.Contains(string(got), `"via":"stream"`) {
				errs <- fmt.Errorf("call %s got %s", n, got)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if s := g.starts(); s != 1 {
		t.Fatalf("remote processes started = %d, want 1", s)
	}
	if n := g.remote.execN.Load(); n != 0 {
		t.Fatalf("exec calls = %d, want 0", n)
	}
}

func TestAcceptanceRPCStreamTimeoutWithLiveStreamKeepsItUp(t *testing.T) {
	var stuckID atomic.Value
	g := newRSRig(t, func(p *rsProc) {
		p.hello()
		go func() {
			for r := range p.reqs {
				if strings.Contains(string(r.Params), `"stuck"`) {
					stuckID.Store(string(r.ID))
					continue
				}
				p.answer(r)
			}
		}()
	})
	// Bring the stream up.
	got, err := g.call(5*time.Second, "warm")
	wantVia(t, got, err, "stream")

	stuckErr := make(chan error, 1)
	go func() {
		_, err := g.call(300*time.Millisecond, "stuck")
		stuckErr <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for stuckID.Load() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the stuck call never reached the remote")
		}
		time.Sleep(2 * time.Millisecond)
	}
	// Another answer arrives after the stuck call was sent.
	got, err = g.call(5*time.Second, "other")
	wantVia(t, got, err, "stream")
	if err := <-stuckErr; err == nil {
		t.Fatal("the unanswered call did not time out")
	}
	p := g.proc(0)
	if p.killed.Load() {
		t.Fatal("a timeout on a stream that is answering killed it")
	}
	// The late answer is dropped; the next call gets its own.
	p.write(fmt.Sprintf(`{"id":%s,"result":{"via":"stream","params":{"n":"late"}}}`, stuckID.Load()))
	got, err = g.call(5*time.Second, "after")
	wantVia(t, got, err, "stream")
	if !strings.Contains(string(got), `"n":"after"`) {
		t.Fatalf("call after the late answer got %s", got)
	}
	if s := g.starts(); s != 1 {
		t.Fatalf("remote processes started = %d, want 1", s)
	}
}

func TestAcceptanceRPCStreamSilentTimeoutKillsAndCoolsDown(t *testing.T) {
	g := newRSRig(t,
		func(p *rsProc) { p.hello() }, // up, then answers nothing
		healthy,
	)
	if _, err := g.call(50*time.Millisecond, "silent"); err == nil {
		t.Fatal("a call on a silent stream succeeded")
	}
	if !g.proc(0).killed.Load() {
		t.Fatal("a timeout on a silent stream did not kill the ssh client")
	}
	g.clock.Add(30 * time.Second)
	g.wantUnreachable(t)
	g.clock.Add(29 * time.Second)
	g.wantUnreachable(t)
	if s := g.starts(); s != 1 {
		t.Fatalf("processes started inside the 60s cooldown = %d, want 1", s)
	}
	g.clock.Add(2 * time.Second)
	got, err := g.call(5*time.Second, "back")
	wantVia(t, got, err, "stream")
	if s := g.starts(); s != 2 {
		t.Fatalf("processes started after the cooldown = %d, want 2", s)
	}
	if n := g.remote.execN.Load(); n != 0 {
		t.Fatalf("exec calls = %d, a stalled stream must never fall back", n)
	}
}

// oldBinaryUsage is what `aiman rpc --stream` prints from a binary without the
// flag, which then exits 1.
const oldBinaryUsage = `{"error":{"code":"invalid_params","message":"usage: aiman rpc METHOD --params-stdin"}}`

func TestAcceptanceRPCStreamOldBinaryFallsBackToExec(t *testing.T) {
	g := newRSRig(t, func(p *rsProc) {
		p.write(oldBinaryUsage)
		go func() {
			time.Sleep(10 * time.Millisecond)
			p.exit(1)
		}()
	})
	got, err := g.call(5*time.Second, "first")
	wantVia(t, got, err, "exec")
	g.remote.mu.Lock()
	cmd := g.remote.cmds[0]
	g.remote.mu.Unlock()
	if !strings.Contains(cmd, "aiman rpc todo.get --params-stdin") {
		t.Fatalf("fallback command = %q", cmd)
	}
	g.clock.Add(10 * time.Minute)
	got, err = g.call(5*time.Second, "second")
	wantVia(t, got, err, "exec")
	if s := g.starts(); s != 1 {
		t.Fatalf("processes started = %d, want 1 (no stream is remembered)", s)
	}
	if n := g.remote.execN.Load(); n != 2 {
		t.Fatalf("exec calls = %d, want 2", n)
	}
}

func TestAcceptanceRPCStreamNoHelloWhileRunningIsAStall(t *testing.T) {
	g := newRSRig(t,
		func(p *rsProc) {}, // running, paging in, never says hello
		healthy,
	)
	_, err := g.call(50*time.Millisecond, "stalled")
	if err == nil {
		t.Fatal("a call with no hello succeeded")
	}
	if errors.Is(err, ErrRPCUnsupported) {
		t.Fatalf("a stall was taken for an old binary: %v", err)
	}
	if !g.proc(0).killed.Load() {
		t.Fatal("a stream with no hello by the deadline was not killed")
	}
	if n := g.remote.execN.Load(); n != 0 {
		t.Fatalf("exec calls = %d, a stall must not fall back", n)
	}
	g.clock.Add(30 * time.Second)
	g.wantUnreachable(t)
	if s := g.starts(); s != 1 {
		t.Fatalf("processes started inside the cooldown = %d, want 1", s)
	}
	g.clock.Add(31 * time.Second)
	got, err := g.call(5*time.Second, "back")
	wantVia(t, got, err, "stream")
	if s := g.starts(); s != 2 {
		t.Fatalf("processes started after the cooldown = %d, want 2 (the stall is not remembered as no stream)", s)
	}
	if n := g.remote.execN.Load(); n != 0 {
		t.Fatalf("exec calls = %d", n)
	}
}

func TestAcceptanceRPCStreamSSHFailureFailsFastNeverExecs(t *testing.T) {
	g := newRSRig(t,
		func(p *rsProc) { p.exit(255) }, // ssh could not connect
		healthy,
	)
	start := time.Now()
	_, err := g.call(5*time.Second, "first")
	if !errors.Is(err, ErrServeUnreachable) {
		t.Fatalf("call over a failed ssh = %v, want ErrServeUnreachable", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("call over a failed ssh took %v, want a fast failure", d)
	}
	g.wantUnreachable(t)
	if s := g.starts(); s != 1 {
		t.Fatalf("processes started inside the backoff = %d, want 1", s)
	}
	if n := g.remote.execN.Load(); n != 0 {
		t.Fatalf("exec calls = %d, an ssh failure must never fall back", n)
	}
	// Exit 255 is not an old binary: after the backoff the stream is tried again.
	g.clock.Add(31 * time.Second)
	got, err := g.call(5*time.Second, "back")
	wantVia(t, got, err, "stream")
	if n := g.remote.execN.Load(); n != 0 {
		t.Fatalf("exec calls = %d", n)
	}
}
