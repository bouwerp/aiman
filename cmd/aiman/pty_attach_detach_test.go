package main

import (
	"errors"
	"io"
	"io/fs"
	"net"
	"strings"
	"testing"
)

// TestAttachExitErrTreatsDetachAsClean pins the contract that made attaching
// from the dashboard fail: a user-initiated detach must exit zero, or `ssh -t`
// reports exit status 1 and the UI shows a failed attach.
func TestAttachExitErrTreatsDetachAsClean(t *testing.T) {
	boom := errors.New("holder went away")
	cases := []struct {
		name     string
		err      error
		detached bool
		wantErr  bool
	}{
		{name: "clean end of stream", err: nil, detached: false, wantErr: false},
		{name: "session exited normally", err: io.EOF, detached: false, wantErr: false},
		// Whichever copy direction notices the detach first is a race, so both
		// of these outcomes happen in practice for the same keypress.
		{name: "detach seen as nil", err: nil, detached: true, wantErr: false},
		{name: "detach seen as closed conn", err: net.ErrClosed, detached: true, wantErr: false},
		{name: "detach seen as wrapped closed conn", err: &net.OpError{Op: "read", Err: net.ErrClosed}, detached: true, wantErr: false},
		// A real failure must still be reported, detach or not.
		{name: "genuine failure", err: boom, detached: false, wantErr: true},
		{name: "genuine failure after detach is moot", err: boom, detached: true, wantErr: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := attachExitErr(tc.err, tc.detached)
			if (got != nil) != tc.wantErr {
				t.Fatalf("attachExitErr(%v, %v) = %v, want error: %v", tc.err, tc.detached, got, tc.wantErr)
			}
		})
	}
}

// closeRecorder stands in for the attach connection.
type closeRecorder struct{ closed bool }

func (c *closeRecorder) Close() error { c.closed = true; return nil }

func TestDetachReaderReportsDetachAndClosesConn(t *testing.T) {
	rec := &closeRecorder{}
	// "hi" then ctrl+q then trailing bytes that must never reach the session.
	src := newChunkReader([]byte{'h', 'i', detachKey, 'X', 'Y'})
	d := detachOnCtrlQ(src, rec)

	buf := make([]byte, 16)
	n, err := d.Read(buf)
	if string(buf[:n]) != "hi" {
		t.Fatalf("bytes before ctrl+q must be delivered, got %q", string(buf[:n]))
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("read ending in ctrl+q must report EOF, got %v", err)
	}
	if !d.Detached() {
		t.Fatal("Detached() must report the ctrl+q so the exit stays clean")
	}
	if !rec.closed {
		t.Fatal("ctrl+q must close the attach connection")
	}
}

func TestDetachReaderPassesThroughWithoutCtrlQ(t *testing.T) {
	rec := &closeRecorder{}
	d := detachOnCtrlQ(newChunkReader([]byte("plain input")), rec)

	buf := make([]byte, 32)
	n, err := d.Read(buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(buf[:n]) != "plain input" {
		t.Fatalf("got %q", string(buf[:n]))
	}
	if d.Detached() {
		t.Fatal("Detached() must stay false without ctrl+q")
	}
	if rec.closed {
		t.Fatal("connection must stay open without ctrl+q")
	}
}

// TestDetachReaderDetachOnFirstByte covers ctrl+q as the only byte read, the
// case where the stdin copy sees no bytes to forward and so returns nil rather
// than a closed-connection error.
func TestDetachReaderDetachOnFirstByte(t *testing.T) {
	rec := &closeRecorder{}
	d := detachOnCtrlQ(newChunkReader([]byte{detachKey}), rec)

	buf := make([]byte, 8)
	n, err := d.Read(buf)
	if n != 0 {
		t.Fatalf("ctrl+q itself must not be forwarded, got %d bytes", n)
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF, got %v", err)
	}
	if !d.Detached() || !rec.closed {
		t.Fatalf("detached=%v closed=%v, want both true", d.Detached(), rec.closed)
	}
}

// chunkReader returns its whole payload in one Read, then EOF, mimicking a tty
// handing over everything currently buffered.
type chunkReader struct {
	data []byte
	done bool
}

func newChunkReader(data []byte) *chunkReader { return &chunkReader{data: data} }

func (c *chunkReader) Read(p []byte) (int, error) {
	if c.done {
		return 0, io.EOF
	}
	c.done = true
	return copy(p, c.data), nil
}

// TestAttachExitErrToleratesSpliceWrappedClose covers the error shape a lost
// stream actually has on Linux. io.Copy(os.Stdout, conn) uses os.File.ReadFrom,
// which splices; a failure on the *source* is reported as a PathError against
// the write target, so the observed message is
// "write /dev/stdout: use of closed network connection". That must not become a
// non-zero exit, or a serve restart looks like a crash to the user.
func TestAttachExitErrToleratesSpliceWrappedClose(t *testing.T) {
	spliced := &fs.PathError{Op: "write", Path: "/dev/stdout", Err: net.ErrClosed}
	if got := attachExitErr(spliced, false); got != nil {
		t.Fatalf("a lost stream must exit cleanly, got %v", got)
	}
	if !strings.Contains(spliced.Error(), "write /dev/stdout: use of closed network connection") {
		t.Fatalf("test no longer reproduces the reported message: %v", spliced)
	}
}

func TestAttachExitNoteDistinguishesDetachFromLostStream(t *testing.T) {
	detached := attachExitNote("abc", true)
	if !strings.Contains(detached, "detached from abc") {
		t.Fatalf("got %q", detached)
	}
	if strings.Contains(detached, "reattach") {
		t.Fatalf("a deliberate detach needs no recovery advice: %q", detached)
	}

	lost := attachExitNote("abc", false)
	for _, want := range []string{"ended", "serve restarted", "aiman pty attach abc"} {
		if !strings.Contains(lost, want) {
			t.Fatalf("lost-stream note missing %q: %q", want, lost)
		}
	}
}
