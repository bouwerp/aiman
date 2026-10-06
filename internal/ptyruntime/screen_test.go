package ptyruntime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// screenFixture returns a manager over a temp root plus a helper that appends to
// the session's spool, standing in for a live session producing output.
func screenFixture(t *testing.T) (*Manager, func(string)) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "pty", "s")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m := NewManagerWithRoot(root, []string{"/bin/true"})
	appendSpool := func(data string) {
		t.Helper()
		f, err := os.OpenFile(filepath.Join(dir, "spool"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(data); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	return m, appendSpool
}

// The whole point: feeding an emulator only what arrived since the last capture
// must produce the same screen as replaying everything from scratch. Rendering
// the full spool every time is linear in the session's entire lifetime — ~1.3s
// for a 6 MB spool on a real remote, against ~10ms for the tmux equivalent.
func TestIncrementalCaptureMatchesFullReplay(t *testing.T) {
	m, appendSpool := screenFixture(t)
	sc := m.screenFor("s")

	frames := []string{
		"first frame\r\n",
		"\x1b[31msecond\x1b[0m frame\r\n",
		"\x1b[2J\x1b[Hrepainted\r\n",
		"tail line\r\n",
	}
	var all strings.Builder
	for _, f := range frames {
		appendSpool(f)
		all.WriteString(f)
		_, got := sc.capture(m.root, "s", 40, 10)
		want := RenderScreen([]byte(all.String()), 40, 10)
		if got != want {
			t.Fatalf("after %q:\n incremental: %q\n full replay: %q", f, got, want)
		}
	}
}

// A capture with nothing new must not re-apply what it already has.
func TestCaptureWithNoNewOutputIsStable(t *testing.T) {
	m, appendSpool := screenFixture(t)
	sc := m.screenFor("s")
	appendSpool("hello\r\n")

	_, first := sc.capture(m.root, "s", 40, 10)
	for i := 0; i < 3; i++ {
		if _, got := sc.capture(m.root, "s", 40, 10); got != first {
			t.Fatalf("capture %d changed with no new output: %q vs %q", i, got, first)
		}
	}
	if !strings.Contains(first, "hello") {
		t.Fatalf("expected the output, got %q", first)
	}
}

// A resize rebuilds rather than reflows: vt10x does not reflow the way the
// agent's own repaint will, and a stale-width screen is worse than a slow one.
func TestCaptureRebuildsOnResize(t *testing.T) {
	m, appendSpool := screenFixture(t)
	sc := m.screenFor("s")
	appendSpool("some output here\r\n")

	sc.capture(m.root, "s", 40, 10)
	_, wide := sc.capture(m.root, "s", 100, 30)

	if wide != RenderScreen([]byte("some output here\r\n"), 100, 30) {
		t.Errorf("resized capture should match a full replay at the new size: %q", wide)
	}
	if sc.cols != 100 || sc.rows != 30 {
		t.Errorf("emulator kept the old size: %dx%d", sc.cols, sc.rows)
	}
}

// Rotation discards the oldest segment, so the byte offset no longer refers to
// the same bytes and the screen has to be rebuilt from what is retained.
func TestCaptureSurvivesSpoolRotation(t *testing.T) {
	m, appendSpool := screenFixture(t)
	sc := m.screenFor("s")
	dir := filepath.Join(m.root, "pty", "s")

	appendSpool(strings.Repeat("old content\r\n", 100))
	sc.capture(m.root, "s", 40, 10)
	before := sc.consumed
	if before == 0 {
		t.Fatal("expected the first capture to consume the spool")
	}

	// Rotate: spool becomes spool.old (there was no previous spool.old), then a
	// fresh spool starts. The stream is unchanged, so this must not lose content.
	if err := os.Rename(filepath.Join(dir, "spool"), filepath.Join(dir, "spool.old")); err != nil {
		t.Fatal(err)
	}
	appendSpool("after rotation\r\n")
	_, got := sc.capture(m.root, "s", 40, 10)
	if !strings.Contains(got, "after rotation") {
		t.Fatalf("post-rotation output missing: %q", got)
	}

	// Rotate again, which does drop the oldest segment: the screen must still be
	// coherent rather than a double-applied mixture.
	if err := os.Rename(filepath.Join(dir, "spool"), filepath.Join(dir, "spool.old")); err != nil {
		t.Fatal(err)
	}
	appendSpool("newest line\r\n")
	_, got = sc.capture(m.root, "s", 40, 10)
	if !strings.Contains(got, "newest line") {
		t.Fatalf("newest output missing after second rotation: %q", got)
	}
	if strings.Count(got, "after rotation") > 1 {
		t.Fatalf("content applied twice after rotation: %q", got)
	}
}

// Attach paints the live screen only. The lines Muse scrolls off the top of
// its transcript region stay in the preview text, above that screen.
func TestViewportOmitsRegionScrollback(t *testing.T) {
	m, appendSpool := screenFixture(t)
	sc := m.screenFor("s")
	var b strings.Builder
	b.WriteString("\x1b[1;5r")
	for i := 1; i <= 9; i++ {
		fmt.Fprintf(&b, "\rHIST%d\n", i)
	}
	b.WriteString("\x1b[r")
	appendSpool(b.String())

	view, all := sc.capture(m.root, "s", 20, 8)
	if strings.Contains(view, "HIST1") {
		t.Fatalf("viewport must stay the live screen, got %q", view)
	}
	if !strings.Contains(all, "HIST1") || strings.Index(all, "HIST1") > strings.Index(all, "HIST9") {
		t.Fatalf("preview text should lead with the scrolled-off lines, got %q", all)
	}
}

func TestScreensAreReapedWhenIdle(t *testing.T) {
	m, _ := screenFixture(t)
	sc := m.screenFor("s")
	sc.lastUsed = time.Now().Add(-2 * screenIdleTTL)

	// Any later lookup reaps first, so the idle entry is gone and a fresh one
	// takes its place.
	if again := m.screenFor("other"); again == nil {
		t.Fatal("expected a screen")
	}
	m.screenMu.Lock()
	_, stillThere := m.screens["s"]
	m.screenMu.Unlock()
	if stillThere {
		t.Error("an idle screen should have been dropped")
	}
}

// Muse keeps the transcript on the primary screen and replaces it with cursor
// addressing when a line leaves the viewport. That line never scrolls off row
// 0, so a replay has nothing to put above the live screen.
func TestCaptureKeepsTranscriptOverwrittenByRedraw(t *testing.T) {
	m, appendSpool := screenFixture(t)
	sc := m.screenFor("s")
	appendSpool(cupPaint(
		"ONE the first transcript line here",
		"TWO the second transcript line here",
		"THREE stays on screen here",
		"",
		"────────",
		"❯ prompt",
	))
	sc.capture(m.root, "s", 80, 8)
	appendSpool(cupPaint(
		"TWO the second transcript line here",
		"THREE stays on screen here",
		"FOUR the new transcript line here",
		"",
		"────────",
		"❯ prompt",
	))

	view, all := sc.capture(m.root, "s", 80, 8)
	if strings.Contains(view, "ONE the first") {
		t.Fatalf("viewport should have dropped the line that left, got %q", view)
	}
	if !strings.Contains(all, "ONE the first") {
		t.Fatalf("preview history should keep the overwritten transcript line, got %q", all)
	}
	if strings.Contains(all, "ONE the first") && strings.Count(all, "ONE the first") != 1 {
		t.Fatalf("overwritten line should be kept once, got %q", all)
	}
}

// Editing the composer rewrites a row in place. That is not transcript history.
func TestCaptureIgnoresComposerEdits(t *testing.T) {
	m, appendSpool := screenFixture(t)
	sc := m.screenFor("s")
	body := []string{
		"ONE the first transcript line here",
		"TWO the second transcript line here",
		"",
		"────────",
	}
	appendSpool(cupPaint(append(append([]string{}, body...), "❯ hello")...))
	sc.capture(m.root, "s", 80, 8)
	appendSpool(cupPaint(append(append([]string{}, body...), "❯ hello world")...))
	_, all := sc.capture(m.root, "s", 80, 8)
	if strings.Contains(all, "❯ hello\n") || strings.Count(all, "❯ hello") > 1 {
		t.Fatalf("composer edits must not enter scrollback, got %q", all)
	}
}

// Spool rotation discards the bytes that painted a line. The preview still has
// to scroll to it: the retained stream can no longer reproduce the scroll.
func TestCaptureScrollbackSurvivesDiscardedSpool(t *testing.T) {
	m, appendSpool := screenFixture(t)
	dir := filepath.Join(m.root, "pty", "s")
	sc := m.screenFor("s")
	appendSpool(cupPaint(
		"ONE the first transcript line here",
		"TWO the second transcript line here",
		"THREE stays on screen here",
		"────────",
		"❯ prompt",
	))
	sc.capture(m.root, "s", 80, 8)
	appendSpool(cupPaint(
		"TWO the second transcript line here",
		"THREE stays on screen here",
		"FOUR the new transcript line here",
		"────────",
		"❯ prompt",
	))
	if _, all := sc.capture(m.root, "s", 80, 8); !strings.Contains(all, "ONE the first") {
		t.Fatalf("line missing before rotation: %q", all)
	}

	// Two rotations drop the segment that contained the redraw.
	rotateSpool(t, dir)
	appendSpool("middle\r\n")
	rotateSpool(t, dir)
	appendSpool("tail line\r\n")
	m.dropScreen("s")

	_, all := m.screenFor("s").capture(m.root, "s", 80, 8)
	if !strings.Contains(all, "ONE the first") {
		t.Fatalf("scrollback was lost with the discarded spool segment: %q", all)
	}
	if !strings.Contains(all, "tail line") {
		t.Fatalf("live output after rotation missing: %q", all)
	}
}

// A full-screen TUI scrolls inside the alternate screen. Lines it overwrites
// belong to that buffer, not to the preview history.
func TestCaptureAltScreenRedrawIsNotScrollback(t *testing.T) {
	m, appendSpool := screenFixture(t)
	sc := m.screenFor("s")
	appendSpool("\x1b[?1049h" + cupPaint(
		"SECRET alt line that should vanish",
		"KEEP this alt row",
		"KEEP this other alt row",
	))
	sc.capture(m.root, "s", 80, 8)
	appendSpool(cupPaint(
		"KEEP this alt row",
		"KEEP this other alt row",
		"FRESH alt row",
	))
	_, all := sc.capture(m.root, "s", 80, 8)
	if strings.Contains(all, "SECRET") {
		t.Fatalf("alt-screen redraw must not be kept as scrollback: %q", all)
	}
}

func TestTakeRedrawnIgnoresLinefeedAndKeepsOverwrite(t *testing.T) {
	m, appendSpool := screenFixture(t)
	// Fill the screen and scroll one row with a real linefeed.
	var b strings.Builder
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&b, "line %d\r\n", i)
	}
	appendSpool(b.String())
	m.screenFor("s").capture(m.root, "s", 40, 8)
	if got := m.TakeRedrawn("s"); len(got) != 0 {
		t.Fatalf("linefeed scroll is the terminal's own history, got %q", got)
	}

	appendSpool(cupPaint(
		"ONE the first transcript line here",
		"TWO the second transcript line here",
		"THREE stays on screen here",
		"────────",
		"❯ prompt",
	))
	m.screenFor("s").capture(m.root, "s", 40, 8)
	m.TakeRedrawn("s")
	appendSpool(cupPaint(
		"TWO the second transcript line here",
		"THREE stays on screen here",
		"FOUR the new transcript line here",
		"────────",
		"❯ prompt",
	))
	m.screenFor("s").capture(m.root, "s", 40, 8)
	got := m.TakeRedrawn("s")
	if len(got) != 1 || !strings.Contains(got[0], "ONE the first") {
		t.Fatalf("overwrite should be pending for attach, got %q", got)
	}
	if again := m.TakeRedrawn("s"); len(again) != 0 {
		t.Fatalf("take should drain, got %q", again)
	}
}

func cupPaint(rows ...string) string {
	var b strings.Builder
	for i, row := range rows {
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[K%s", i+1, row)
	}
	return b.String()
}

func rotateSpool(t *testing.T, dir string) {
	t.Helper()
	if err := os.Rename(filepath.Join(dir, "spool"), filepath.Join(dir, "spool.old")); err != nil {
		t.Fatal(err)
	}
}

func TestDropScreenForgetsState(t *testing.T) {
	m, appendSpool := screenFixture(t)
	appendSpool("before forget\r\n")
	if got, err := m.CaptureScreen("s"); err != nil || !strings.Contains(got, "before forget") {
		t.Fatalf("capture: %q / %v", got, err)
	}
	m.dropScreen("s")
	m.screenMu.Lock()
	_, ok := m.screens["s"]
	m.screenMu.Unlock()
	if ok {
		t.Error("dropScreen should remove the emulator so a reused id cannot inherit it")
	}
}
