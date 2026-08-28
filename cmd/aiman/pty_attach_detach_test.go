package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"strings"
	"testing"
	"time"
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

func TestTrailingCtrlQPrefixOnlyHoldsOnceQIsIdentified(t *testing.T) {
	cases := []struct {
		in   string
		hold int
	}{
		{"\x1b[A", 0},
		{"\x1b[", 0},
		{"\x1b[1;5A", 0},
		{"\x1b[11~", 0},
		{"\x1b[113", 5},
		{"\x1b[113;5", 7},
		{"\x1b[27;5;11", 0},
		{"\x1b[27;5;113", 10},
	}
	for _, tc := range cases {
		if got := trailingCtrlQPrefix([]byte(tc.in)); got != tc.hold {
			t.Errorf("trailingCtrlQPrefix(%q) = %d, want %d", tc.in, got, tc.hold)
		}
	}
}

func TestAttachRedrawNudgeIsARealSizeChange(t *testing.T) {
	cases := []struct {
		cols, rows, wantCols, wantRows int
		ok                             bool
	}{
		{80, 24, 78, 23, true},
		{160, 48, 106, 32, true},
		{3, 24, 3, 23, true},
		{1, 24, 1, 23, true},
		{0, 24, 0, 0, false},
		{80, 0, 0, 0, false},
	}
	for _, tc := range cases {
		gotCols, gotRows, ok := attachRedrawNudge(tc.cols, tc.rows)
		if ok != tc.ok || gotCols != tc.wantCols || gotRows != tc.wantRows {
			t.Errorf("attachRedrawNudge(%d,%d)=%d,%d,%v want %d,%d,%v",
				tc.cols, tc.rows, gotCols, gotRows, ok, tc.wantCols, tc.wantRows, tc.ok)
		}
	}
}

func TestKickAttachRedrawSendsNudgeThenRestore(t *testing.T) {
	var sizes []string
	var sleeps []time.Duration
	kickAttachRedraw(func(cols, rows int) error {
		sizes = append(sizes, fmt.Sprintf("%dx%d", cols, rows))
		return nil
	}, 80, 24, func(d time.Duration) { sleeps = append(sleeps, d) })
	if len(sizes) != 2 || sizes[0] != "78x23" || sizes[1] != "80x24" {
		t.Fatalf("kick must send two distinct sizes, got %v", sizes)
	}
	if len(sleeps) != 2 || sleeps[0] != attachRedrawLead || sleeps[1] != attachRedrawGap {
		t.Fatalf("lead then debounce gap, got %v", sleeps)
	}
	if attachRedrawGap < 500*time.Millisecond {
		t.Fatalf("attachRedrawGap %s is shorter than Ink-style SIGWINCH debounce; the agent would only see the restored size", attachRedrawGap)
	}
}

func TestKickAttachRedrawSkipsUnusableSizes(t *testing.T) {
	called := 0
	kickAttachRedraw(func(int, int) error {
		called++
		return nil
	}, 0, 24, func(time.Duration) {})
	if called != 0 {
		t.Fatalf("unusable size must not resize, calls=%d", called)
	}
}

func TestAttachGrowBoxExpandsFromCenter(t *testing.T) {
	x0, y0, w0, h0 := attachGrowBox(80, 24, 0, attachGrowSteps)
	x1, y1, w1, h1 := attachGrowBox(80, 24, attachGrowSteps-1, attachGrowSteps)
	if w0 >= w1 || h0 >= h1 {
		t.Fatalf("box must grow, start %dx%d end %dx%d", w0, h0, w1, h1)
	}
	if w1 != 80 || h1 != 24 {
		t.Fatalf("last frame must fill the tty, got %dx%d", w1, h1)
	}
	if x1 != 1 || y1 != 1 {
		t.Fatalf("full frame origin must be 1,1 got %d,%d", x1, y1)
	}
	if x0 <= x1 || y0 <= y1 {
		t.Fatalf("origin must move toward the corner as the box grows, start %d,%d end %d,%d", x0, y0, x1, y1)
	}
}

func TestAttachGrowFrameUsesCUPNotBareLF(t *testing.T) {
	got := attachGrowFrame(40, 12, 4, attachGrowSteps)
	if strings.Contains(got, "\n") {
		t.Fatalf("raw-mode frames must not use bare LF: %q", got)
	}
	if !strings.Contains(got, "\x1b[") || !strings.Contains(got, "╭") {
		t.Fatalf("frame must CUP-draw a box, got %q", got)
	}
}

func TestAttachOpenEntersAltScreenAndClears(t *testing.T) {
	got := attachOpen()
	for _, want := range []string{"\x1b[?1049h", "\x1b[2J", "\x1b[H"} {
		if !strings.Contains(got, want) {
			t.Errorf("attach open missing %q: %q", want, got)
		}
	}
}

func TestAttachCloseLeavesAltScreen(t *testing.T) {
	got := attachClose()
	if !strings.Contains(got, "\x1b[?1049l") {
		t.Errorf("attach close must leave the alt screen, got %q", got)
	}
	for _, mode := range []string{"1000", "1002", "1006"} {
		if !strings.Contains(got, "?"+mode+"l") {
			t.Errorf("attach close missing mouse off ?%sl", mode)
		}
	}
}

func TestMouseTrackingOnEnablesSGRWheel(t *testing.T) {
	on := mouseTrackingOn()
	for _, want := range []string{"\x1b[?1000h", "\x1b[?1002h", "\x1b[?1006h"} {
		if !strings.Contains(on, want) {
			t.Errorf("mouse on missing %q: %q", want, on)
		}
	}
	if strings.Contains(on, "1003h") {
		t.Error("any-motion tracking is too noisy for attach")
	}
}

func TestMouseTrackingOffDisablesWhatOnEnabled(t *testing.T) {
	on, off := mouseTrackingOn(), mouseTrackingOff()
	for _, mode := range []string{"1000", "1002", "1006"} {
		if !strings.Contains(on, "?"+mode+"h") {
			t.Errorf("on missing ?%sh", mode)
		}
		if !strings.Contains(off, "?"+mode+"l") {
			t.Errorf("off missing ?%sl", mode)
		}
	}
}

func TestDetachReaderPassesThroughSGRWheel(t *testing.T) {
	rec := &closeRecorder{}
	wheel := []byte("\x1b[<64;12;8M")
	d := detachOnCtrlQ(newChunkReader(wheel), rec)
	buf := make([]byte, 32)
	n, err := d.Read(buf)
	if err != nil || !bytes.Equal(buf[:n], wheel) || d.Detached() {
		t.Fatalf("wheel must reach the agent, got %q err=%v detached=%v", buf[:n], err, d.Detached())
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
