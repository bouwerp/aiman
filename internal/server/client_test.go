package server

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// AttachDial used to wrap the socket in a throwaway bufio.Reader just to take
// the handshake line, then Relay read from the raw connection. Anything the
// server wrote in the same packet as `pty_attached` — a Grok/Claude first
// frame is typically a few KB of alt-screen, mouse, and box-drawing — sat in
// that discarded buffer. The attached TUI then had no borders, no mouse
// scroll, and a layout that never filled the screen.
func TestAttachDialPreservesBytesAfterHandshake(t *testing.T) {
	dir := shortTempDir(t)
	sock := SocketPath(dir)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	const payload = "\x1b[?1049h\x1b[?1000h┌────────┐FIRST-FRAME"

	var srvMu sync.Mutex
	var srv net.Conn
	go func() {
		c, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		srvMu.Lock()
		srv = c
		srvMu.Unlock()
		buf := make([]byte, 4096)
		_, _ = c.Read(buf)
		// One write so the client's handshake reader is guaranteed to pull
		// payload bytes into its buffer along with the JSON line.
		_, _ = c.Write([]byte(`{"id":"x","result":{"type":"pty_attached"}}` + "\n" + payload))
	}()
	t.Cleanup(func() {
		srvMu.Lock()
		defer srvMu.Unlock()
		if srv != nil {
			_ = srv.Close()
		}
	})

	a, err := AttachDial(sock, "s", 120, 40)
	if err != nil {
		t.Fatalf("AttachDial: %v", err)
	}
	defer a.Close()

	var out bytes.Buffer
	var outMu sync.Mutex
	stdinR, stdinW := io.Pipe()
	defer stdinW.Close()
	done := make(chan error, 1)
	go func() {
		done <- a.Relay(stdinR, &lockedWriter{mu: &outMu, w: &out})
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		outMu.Lock()
		got := out.String()
		outMu.Unlock()
		if bytes.Contains([]byte(got), []byte(payload)) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	outMu.Lock()
	got := out.String()
	outMu.Unlock()
	t.Fatalf("first TUI frame after handshake was dropped; got %q", got)
}

// Ensure a nil-reader AttachConn (as unit tests construct) still relays.
func TestRelayReadsRawConnWhenNoHandshakeReader(t *testing.T) {
	local, remote := unixSocketPair(t)
	defer remote.Close()

	a := &AttachConn{conn: local}
	stdinR, stdinW := io.Pipe()
	defer stdinW.Close()
	var out bytes.Buffer
	var outMu sync.Mutex
	done := make(chan error, 1)
	go func() { done <- a.Relay(stdinR, &lockedWriter{mu: &outMu, w: &out}) }()
	if _, err := remote.Write([]byte("hello-raw")); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		outMu.Lock()
		got := out.String()
		outMu.Unlock()
		if got == "hello-raw" {
			_ = remote.Close()
			select {
			case <-done:
			case <-time.After(time.Second):
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = remote.Close()
	outMu.Lock()
	got := out.String()
	outMu.Unlock()
	t.Fatalf("got %q, want hello-raw", got)
}
