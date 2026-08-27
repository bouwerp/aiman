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
func TestDetachReaderTreatsKittyCtrlQAsDetach(t *testing.T) {
	rec := &closeRecorder{}
	// agy (and other Ink TUIs) enable the kitty keyboard protocol, so ctrl+q
	// arrives as CSI 113;5u rather than 0x11.
	src := newChunkReader(append([]byte("hi"), []byte("\x1b[113;5uXY")...))
	d := detachOnCtrlQ(src, rec)

	buf := make([]byte, 16)
	n, err := d.Read(buf)
	if string(buf[:n]) != "hi" {
		t.Fatalf("bytes before ctrl+q must be delivered, got %q", string(buf[:n]))
	}
	if !errors.Is(err, io.EOF) || !d.Detached() || !rec.closed {
		t.Fatalf("kitty ctrl+q must detach: n=%d err=%v detached=%v closed=%v", n, err, d.Detached(), rec.closed)
	}
}

func TestDetachReaderTreatsModifyOtherKeysCtrlQAsDetach(t *testing.T) {
	rec := &closeRecorder{}
	src := newChunkReader([]byte("\x1b[27;5;113~"))
	d := detachOnCtrlQ(src, rec)

	buf := make([]byte, 16)
	n, err := d.Read(buf)
	if n != 0 || !errors.Is(err, io.EOF) || !d.Detached() {
		t.Fatalf("xterm modifyOtherKeys ctrl+q must detach: n=%d err=%v detached=%v", n, err, d.Detached())
	}
}

func TestDetachReaderKittyCtrlQSplitAcrossReads(t *testing.T) {
	rec := &closeRecorder{}
	src := &seqReader{chunks: [][]byte{[]byte("pre\x1b[113"), []byte(";5u")}}
	d := detachOnCtrlQ(src, rec)

	buf := make([]byte, 16)
	n, err := d.Read(buf)
	if string(buf[:n]) != "pre" {
		t.Fatalf("first read should flush bytes before the CSI prefix, got %q", string(buf[:n]))
	}
	if err != nil {
		t.Fatalf("incomplete CSI must not EOF yet: %v", err)
	}

	n, err = d.Read(buf)
	if n != 0 || !errors.Is(err, io.EOF) || !d.Detached() || !rec.closed {
		t.Fatalf("second read must finish the detach: n=%d err=%v detached=%v closed=%v", n, err, d.Detached(), rec.closed)
	}
}

func TestDetachReaderPassesThroughUnrelatedCSI(t *testing.T) {
	rec := &closeRecorder{}
	d := detachOnCtrlQ(newChunkReader([]byte("\x1b[A")), rec)
	buf := make([]byte, 16)
	n, err := d.Read(buf)
	if err != nil || string(buf[:n]) != "\x1b[A" || d.Detached() {
		t.Fatalf("arrow-up must pass through, got %q err=%v detached=%v", string(buf[:n]), err, d.Detached())
	}
}

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

// seqReader returns one chunk per Read, then EOF.
type seqReader struct {
	chunks [][]byte
}

func (s *seqReader) Read(p []byte) (int, error) {
	if len(s.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, s.chunks[0])
	s.chunks = s.chunks[1:]
	return n, nil
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

// A banner is written into the middle of a full-screen agent's frame, so it has
// to erase the rest of the row. Without that, the remainder of the agent's box
// border trails off the end of the message — the long run of "─" seen after a
// detach — and the text inherits whatever colours the agent left set.
func TestNoticeCleansUpAfterItself(t *testing.T) {
	got := notice("[aiman] detached from abc")

	if !strings.HasPrefix(got, "\x1b[0m") {
		t.Errorf("must reset inherited styling first: %q", got)
	}
	if !strings.Contains(got, "\x1b[2K") {
		t.Errorf("must erase the line it writes on: %q", got)
	}
	// The erase-to-end must come after the text, or the border survives.
	textAt := strings.Index(got, "[aiman] detached from abc")
	eraseAt := strings.Index(got, "\x1b[K"+"\r\n")
	if textAt < 0 || eraseAt < textAt {
		t.Errorf("must erase the rest of the row after the text: %q", got)
	}
	if !strings.HasSuffix(got, "\r\n") {
		t.Errorf("raw mode needs an explicit carriage return: %q", got)
	}
	// Raw mode: every newline must carry a carriage return.
	if strings.Count(got, "\n") != strings.Count(got, "\r\n") {
		t.Errorf("bare newline in raw mode: %q", got)
	}
}

// The last thing printed clears the screen: ssh's "Connection closed" and the
// dashboard redraw both land on whatever is left behind.
func TestExitNoticeClearsTheScreen(t *testing.T) {
	got := exitNotice("[aiman] detached from abc")
	for _, want := range []string{"\x1b[0m", "\x1b[2J", "\x1b[H"} {
		if !strings.Contains(got, want) {
			t.Errorf("exit notice missing %q: %q", want, got)
		}
	}
	if !strings.Contains(got, "[aiman] detached from abc") {
		t.Errorf("text lost: %q", got)
	}
	if strings.Count(got, "\n") != strings.Count(got, "\r\n") {
		t.Errorf("bare newline in raw mode: %q", got)
	}
}
