package server

import (
	"errors"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"
)

// TestRelayDetachErrorSurface pins the error surface that `aiman pty attach`
// relies on to tell a detach apart from a failure.
//
// Detaching works by closing the connection from the input side. Relay runs two
// io.Copy directions and returns whichever finishes first, so the same keypress
// legitimately produces either nil or a closed-connection error depending on
// which goroutine wins. cmd/aiman's attachExitErr tolerates exactly that set;
// if Relay ever reports a closed connection as some *other* error, a clean
// detach silently becomes a non-zero exit again, which over `ssh -t` reaches the
// dashboard as "failed to attach". This test fails loudly if that set changes.
func TestRelayDetachErrorSurface(t *testing.T) {
	sawNil, sawClosed := false, false
	// The outcome is a goroutine race, so sample it rather than assuming one.
	for i := 0; i < 50; i++ {
		err := relayThenCloseFromReader(t, i%2 == 0)
		switch {
		case err == nil:
			sawNil = true
		case errors.Is(err, net.ErrClosed):
			sawClosed = true
		case errors.Is(err, io.EOF):
			// Also benign, and already tolerated.
		default:
			t.Fatalf("iteration %d: detach produced an error outside the tolerated set: %v (%T)", i, err, err)
		}
	}
	if !sawNil && !sawClosed {
		t.Fatal("expected at least one detach to report nil or a closed connection")
	}
	t.Logf("observed nil=%v closed-conn=%v across 50 detaches", sawNil, sawClosed)
}

// relayThenCloseFromReader runs a real Relay over a unix socket pair and closes
// the connection from inside the input reader, exactly as the ctrl+q handler
// does. withPayload controls whether bytes precede the close, which is what
// decides whether the input copy has anything to fail on.
//
// The transport must be a unix socket, not net.Pipe: a pipe reports closure as
// io.ErrClosedPipe while a socket reports net.ErrClosed, and it is the socket's
// behaviour that production depends on.
func relayThenCloseFromReader(t *testing.T, withPayload bool) error {
	t.Helper()
	local, remote := unixSocketPair(t)
	defer remote.Close()

	a := &AttachConn{conn: local}
	// Keep the far end drained so the input copy is never blocked on the write.
	go func() { _, _ = io.Copy(io.Discard, remote) }()

	in := &closeMidStreamReader{closer: a, withPayload: withPayload}
	done := make(chan error, 1)
	go func() { done <- a.Relay(in, io.Discard) }()

	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Relay did not return after the reader closed the connection")
		return nil
	}
}

// unixSocketPair returns two connected unix-socket ends, the transport the
// attach stream actually uses.
func unixSocketPair(t *testing.T) (client, server net.Conn) {
	t.Helper()
	path := filepath.Join(shortTempDir(t), "relay.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, aerr := ln.Accept()
		if aerr != nil {
			accepted <- nil
			return
		}
		accepted <- c
	}()
	client, err = net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	server = <-accepted
	if server == nil {
		t.Fatal("accept failed")
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, server
}

// closeMidStreamReader mimics detachReader: it optionally yields a few bytes,
// closes the connection, and reports EOF.
type closeMidStreamReader struct {
	closer      io.Closer
	withPayload bool
	done        bool
}

func (c *closeMidStreamReader) Read(p []byte) (int, error) {
	if c.done {
		return 0, io.EOF
	}
	c.done = true
	n := 0
	if c.withPayload {
		n = copy(p, "keystrokes")
	}
	_ = c.closer.Close()
	return n, io.EOF
}
