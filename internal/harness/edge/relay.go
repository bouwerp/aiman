package edge

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// relay is one listener on the outer address and the connections it carries.
// Each accepted connection gets its own dial to the target; bytes are copied
// both ways with half-close, so a client that shuts its write side still reads
// the reply.
type relay struct {
	spec        RelaySpec
	ln          net.Listener
	dialTimeout time.Duration
	ctx         context.Context // ends at close, so a pending dial stops
	cancel      context.CancelFunc

	active atomic.Int64
	total  atomic.Int64
	failed atomic.Int64

	mu      sync.Mutex
	lastErr string
	conns   map[net.Conn]struct{}
	closed  bool
	wg      sync.WaitGroup
}

func newRelay(spec RelaySpec, ln net.Listener, dialTimeout time.Duration) *relay {
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		spec.Port = tcp.Port
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &relay{spec: spec, ln: ln, dialTimeout: dialTimeout, ctx: ctx, cancel: cancel, conns: map[net.Conn]struct{}{}}
}

func (r *relay) status() RelayStatus {
	r.mu.Lock()
	last := r.lastErr
	r.mu.Unlock()
	return RelayStatus{
		RelaySpec: r.spec,
		Listen:    r.ln.Addr().String(),
		Active:    r.active.Load(),
		Total:     r.total.Load(),
		Failed:    r.failed.Load(),
		LastError: last,
	}
}

func (r *relay) serve() {
	for {
		c, err := r.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			r.setErr(err)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if !r.track(c, true) {
			_ = c.Close()
			return
		}
		go r.handle(c)
	}
}

// track records a live connection so close can end it; false once closed.
// handler counts a new handler under the same lock, so close never waits on a
// group that a racing accept is about to add to.
func (r *relay) track(c net.Conn, handler bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	r.conns[c] = struct{}{}
	if handler {
		r.wg.Add(1)
	}
	return true
}

func (r *relay) untrack(c net.Conn) {
	r.mu.Lock()
	delete(r.conns, c)
	r.mu.Unlock()
}

func (r *relay) setErr(err error) {
	r.mu.Lock()
	r.lastErr = err.Error()
	r.mu.Unlock()
}

func (r *relay) handle(client net.Conn) {
	defer r.wg.Done()
	defer r.untrack(client)
	defer func() { _ = client.Close() }()
	r.total.Add(1)
	r.active.Add(1)
	defer r.active.Add(-1)

	ctx, cancel := context.WithTimeout(r.ctx, r.dialTimeout)
	var d net.Dialer
	upstream, err := d.DialContext(ctx, "tcp", r.spec.Target)
	cancel()
	if err != nil {
		r.failed.Add(1)
		r.setErr(err)
		return
	}
	if !r.track(upstream, false) {
		_ = upstream.Close()
		return
	}
	defer r.untrack(upstream)
	defer func() { _ = upstream.Close() }()

	done := make(chan struct{}, 2)
	go pipe(upstream, client, done)
	go pipe(client, upstream, done)
	<-done
	<-done
}

// pipe copies src to dst, then half-closes dst's write side so the peer sees
// EOF while the other direction keeps flowing.
func pipe(dst, src net.Conn, done chan<- struct{}) {
	_, _ = io.Copy(dst, src)
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	} else {
		_ = dst.Close()
	}
	done <- struct{}{}
}

// close stops accepting, ends every live connection and waits for the
// handlers.
func (r *relay) close() {
	_ = r.ln.Close()
	r.cancel()
	r.mu.Lock()
	r.closed = true
	for c := range r.conns {
		_ = c.Close()
	}
	r.mu.Unlock()
	r.wg.Wait()
}
