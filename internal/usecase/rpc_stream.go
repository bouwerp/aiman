package usecase

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrServeUnreachable means the remote's RPC stream is down: it is cooling down
// after a stall or backing off after it died. Such a call never falls back to a
// per-call exec, because that is what piles processes up on a struggling host.
var ErrServeUnreachable = errors.New("aiman serve on the remote is unreachable: rpc stream down")

// RPCStreamProcess is one started `ssh … exec aiman rpc --stream`.
type RPCStreamProcess interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	// Wait blocks until the process exits and returns its exit status, -1 when
	// it was killed.
	Wait() int
	Kill() error
}

// RPCStreamRemote is a remote that can carry an RPC stream. Calls whose
// remotes share a key share one stream.
type RPCStreamRemote interface {
	TerminalExecutor
	RPCStreamKey() string
}

// rpcStreamCommander builds the stream's ssh command; ssh.Manager implements it.
type rpcStreamCommander interface {
	RPCStreamCommand() *exec.Cmd
}

const (
	// rpcStreamStallCooldown is how long a remote stays down after a call timed
	// out on a stream that wrote nothing since the call was sent.
	rpcStreamStallCooldown = 60 * time.Second
	// rpcSSHExitStatus is ssh's own failure status: the connection, not the
	// remote command, failed.
	rpcSSHExitStatus = 255
	// rpcStreamWriteQueue bounds request lines waiting for the stream's stdin.
	rpcStreamWriteQueue = 256
)

// rpcStreamBackoff is the wait before restarting a stream that died, by the
// number of deaths since the last hello.
var rpcStreamBackoff = []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 30 * time.Second}

// RPCStreams carries serve RPC calls over one long-lived stream per remote.
type RPCStreams struct {
	// Now is the clock for cooldowns and backoff; nil is time.Now.
	Now func() time.Time
	// Start starts a remote's stream process; nil wraps RPCStreamCommand.
	Start func(remote RPCStreamRemote) (RPCStreamProcess, error)

	mu      sync.Mutex
	remotes map[string]*rpcRemoteState
}

// rpcRemoteState is what RPCStreams remembers about one key.
type rpcRemoteState struct {
	cur       *rpcStream
	downUntil time.Time
	deaths    int
	// noStream: the remote binary has no --stream, so calls exec.
	noStream bool
	// serveVersion is the last serve version a probe reported.
	serveVersion string
}

// NewRPCStreams returns an empty set of streams.
func NewRPCStreams() *RPCStreams {
	return &RPCStreams{remotes: map[string]*rpcRemoteState{}}
}

// defaultRPCStreams carries ServeRPC.
var defaultRPCStreams = NewRPCStreams()

// NoteServeVersion records the serve version a daemon probe of host reported.
// When it differs from the last one seen, the remembered "no stream" answer for
// that host is dropped, so the next call tries the stream again.
func NoteServeVersion(host, version string) {
	defaultRPCStreams.NoteServeVersion(host, version)
}

// NoteServeVersion is the package function on s. host matches a key equal to
// it or ending in "@"+host.
func (s *RPCStreams) NoteServeVersion(host, version string) {
	if host == "" || version == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, st := range s.remotes {
		if key != host && !strings.HasSuffix(key, "@"+host) {
			continue
		}
		if st.serveVersion != "" && st.serveVersion != version {
			st.noStream = false
		}
		st.serveVersion = version
	}
}

func (s *RPCStreams) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Call answers like ServeRPC. A remote that is not an RPCStreamRemote, or whose
// binary has no --stream, is called by exec.
func (s *RPCStreams) Call(ctx context.Context, remote TerminalExecutor, method string, params any) (json.RawMessage, error) {
	sr, ok := remote.(RPCStreamRemote)
	if !ok {
		return execServeRPC(ctx, remote, method, params)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("encode %s params: %w", method, err)
	}
	st, useExec, err := s.stream(sr)
	if useExec {
		return execServeRPC(ctx, remote, method, params)
	}
	if err != nil {
		return nil, err
	}

	select {
	case <-st.hello:
	case <-st.exited:
		if st.oldBinary() {
			return execServeRPC(ctx, remote, method, params)
		}
		return nil, fmt.Errorf("rpc %s: %w", method, ErrServeUnreachable)
	case <-ctx.Done():
		// A running process with no hello is paging in or stuck, never an old
		// binary.
		s.stall(sr.RPCStreamKey(), st)
		return nil, fmt.Errorf("rpc %s: no hello from the rpc stream: %w", method, ctx.Err())
	}
	return s.callOn(ctx, sr.RPCStreamKey(), st, method, raw)
}

// stream returns the live stream for remote, starting one when none runs and
// the remote is not down. useExec reports a remote without --stream.
func (s *RPCStreams) stream(remote RPCStreamRemote) (st *rpcStream, useExec bool, err error) {
	key := remote.RPCStreamKey()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.remotes == nil {
		s.remotes = map[string]*rpcRemoteState{}
	}
	rs := s.remotes[key]
	if rs == nil {
		rs = &rpcRemoteState{}
		s.remotes[key] = rs
	}
	if rs.noStream {
		return nil, true, nil
	}
	if rs.cur != nil {
		return rs.cur, false, nil
	}
	if s.now().Before(rs.downUntil) {
		return nil, false, ErrServeUnreachable
	}
	start := s.Start
	if start == nil {
		if _, ok := remote.(rpcStreamCommander); !ok {
			return nil, true, nil
		}
		start = startRPCStreamCommand
	}
	proc, serr := start(remote)
	if serr != nil {
		s.backoffLocked(rs)
		return nil, false, fmt.Errorf("%w: start: %v", ErrServeUnreachable, serr)
	}
	st = newRPCStream(proc)
	rs.cur = st
	go st.run(func(code int) { s.ended(key, st, code) })
	return st, false, nil
}

// backoffLocked marks a remote down after a death. s.mu is held.
func (s *RPCStreams) backoffLocked(rs *rpcRemoteState) {
	d := rpcStreamBackoff[min(rs.deaths, len(rpcStreamBackoff)-1)]
	rs.deaths++
	if until := s.now().Add(d); until.After(rs.downUntil) {
		rs.downUntil = until
	}
}

// ended runs when st's process has exited and its output is drained.
func (s *RPCStreams) ended(key string, st *rpcStream, code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs := s.remotes[key]
	if rs == nil || rs.cur != st {
		// Killed for a stall: the cooldown is already set.
		return
	}
	rs.cur = nil
	if !st.sawHello.Load() && code != rpcSSHExitStatus && code != -1 {
		rs.noStream = true
		return
	}
	s.backoffLocked(rs)
}

// stall kills st and holds its remote down for the cooldown.
func (s *RPCStreams) stall(key string, st *rpcStream) {
	s.mu.Lock()
	if rs := s.remotes[key]; rs != nil && rs.cur == st {
		rs.cur = nil
		rs.downUntil = s.now().Add(rpcStreamStallCooldown)
	}
	s.mu.Unlock()
	st.kill()
}

// callOn sends one call on a stream that has said hello and waits for its id.
func (s *RPCStreams) callOn(ctx context.Context, key string, st *rpcStream, method string, params json.RawMessage) (json.RawMessage, error) {
	id, ch := st.register()
	defer st.unregister(id)
	line, err := json.Marshal(struct {
		ID     string          `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}{id, method, params})
	if err != nil {
		return nil, fmt.Errorf("encode %s request: %w", method, err)
	}
	seen := st.lines.Load()
	select {
	case st.writes <- append(line, '\n'):
	case <-st.exited:
		return nil, fmt.Errorf("rpc %s: %w", method, ErrServeUnreachable)
	case <-ctx.Done():
		s.timedOut(key, st, seen)
		return nil, fmt.Errorf("rpc %s: %w", method, ctx.Err())
	}
	select {
	case ans := <-ch:
		return rpcAnswerResult(ans)
	case <-st.exited:
		return nil, fmt.Errorf("rpc %s: rpc stream ended: %w", method, ErrServeUnreachable)
	case <-ctx.Done():
		s.timedOut(key, st, seen)
		return nil, fmt.Errorf("rpc %s: %w", method, ctx.Err())
	}
}

// timedOut handles a call's deadline: a stream that wrote no line since the
// call was sent is stalled.
func (s *RPCStreams) timedOut(key string, st *rpcStream, seen int64) {
	if st.lines.Load() == seen {
		s.stall(key, st)
	}
}

// rpcStream is one running stream process.
type rpcStream struct {
	proc   RPCStreamProcess
	writes chan []byte
	hello  chan struct{}
	exited chan struct{}
	// code is the exit status, set before exited closes.
	code     int
	sawHello atomic.Bool
	killed   atomic.Bool
	// lines counts every stdout line, so a timeout can tell a silent stream.
	lines atomic.Int64

	mu      sync.Mutex
	nextID  int64
	pending map[string]chan rpcAnswer
}

func newRPCStream(proc RPCStreamProcess) *rpcStream {
	return &rpcStream{
		proc:    proc,
		writes:  make(chan []byte, rpcStreamWriteQueue),
		hello:   make(chan struct{}),
		exited:  make(chan struct{}),
		pending: map[string]chan rpcAnswer{},
	}
}

// oldBinary reports a remote command that exited on its own before the hello
// with a status other than ssh's: a binary without --stream. Valid once
// exited is closed.
func (st *rpcStream) oldBinary() bool {
	return !st.sawHello.Load() && !st.killed.Load() && st.code != rpcSSHExitStatus && st.code != -1
}

func (st *rpcStream) kill() {
	st.killed.Store(true)
	_ = st.proc.Kill()
}

func (st *rpcStream) register() (string, chan rpcAnswer) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.nextID++
	id := strconv.FormatInt(st.nextID, 10)
	ch := make(chan rpcAnswer, 1)
	st.pending[id] = ch
	return id, ch
}

func (st *rpcStream) unregister(id string) {
	st.mu.Lock()
	delete(st.pending, id)
	st.mu.Unlock()
}

// run writes queued requests and reads answers until the process exits, then
// calls done with its exit status before closing exited.
func (st *rpcStream) run(done func(code int)) {
	stopWrites := make(chan struct{})
	go st.writeLoop(stopWrites)
	st.readLoop()
	close(stopWrites)
	st.code = st.proc.Wait()
	done(st.code)
	close(st.exited)
}

func (st *rpcStream) writeLoop(stop <-chan struct{}) {
	in := st.proc.Stdin()
	defer func() { _ = in.Close() }()
	for {
		select {
		case <-stop:
			return
		case line := <-st.writes:
			if _, err := in.Write(line); err != nil {
				st.kill()
				return
			}
		}
	}
}

func (st *rpcStream) readLoop() {
	sc := bufio.NewScanner(st.proc.Stdout())
	sc.Buffer(make([]byte, 0, 64<<10), 64<<20)
	for sc.Scan() {
		st.lines.Add(1)
		st.dispatch(sc.Bytes())
	}
	// Drain whatever is left so the process is never blocked on a full pipe.
	_, _ = io.Copy(io.Discard, st.proc.Stdout())
}

// streamLine is any line the remote writes: the hello or an answer.
type streamLine struct {
	Stream int             `json:"stream"`
	ID     json.RawMessage `json:"id"`
	rpcAnswer
}

func (st *rpcStream) dispatch(line []byte) {
	var l streamLine
	if json.Unmarshal(line, &l) != nil {
		return
	}
	if len(l.ID) == 0 {
		if l.Stream == 1 && !st.sawHello.Swap(true) {
			close(st.hello)
		}
		return
	}
	if !st.sawHello.Load() {
		return
	}
	id := strings.Trim(string(l.ID), `"`)
	st.mu.Lock()
	ch := st.pending[id]
	delete(st.pending, id)
	st.mu.Unlock()
	if ch != nil {
		ch <- l.rpcAnswer
	}
}

// startRPCStreamCommand wraps the remote's RPCStreamCommand.
func startRPCStreamCommand(remote RPCStreamRemote) (RPCStreamProcess, error) {
	c, ok := remote.(rpcStreamCommander)
	if !ok {
		return nil, errors.New("remote has no rpc stream command")
	}
	cmd := c.RPCStreamCommand()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &cmdStreamProcess{cmd: cmd, stdin: stdin, stdout: stdout}, nil
}

// cmdStreamProcess is an RPCStreamProcess over an exec.Cmd.
type cmdStreamProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.Reader
	once   sync.Once
	code   int
}

func (p *cmdStreamProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *cmdStreamProcess) Stdout() io.Reader     { return p.stdout }
func (p *cmdStreamProcess) Kill() error           { return p.cmd.Process.Kill() }

func (p *cmdStreamProcess) Wait() int {
	p.once.Do(func() {
		_ = p.cmd.Wait()
		p.code = -1
		if st := p.cmd.ProcessState; st != nil {
			p.code = st.ExitCode()
		}
	})
	return p.code
}
