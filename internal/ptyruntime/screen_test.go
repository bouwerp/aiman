package ptyruntime

import (
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
		got := sc.capture(m.root, "s", 40, 10)
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

	first := sc.capture(m.root, "s", 40, 10)
	for i := 0; i < 3; i++ {
		if got := sc.capture(m.root, "s", 40, 10); got != first {
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
	wide := sc.capture(m.root, "s", 100, 30)

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
	got := sc.capture(m.root, "s", 40, 10)
	if !strings.Contains(got, "after rotation") {
		t.Fatalf("post-rotation output missing: %q", got)
	}

	// Rotate again, which does drop the oldest segment: the screen must still be
	// coherent rather than a double-applied mixture.
	if err := os.Rename(filepath.Join(dir, "spool"), filepath.Join(dir, "spool.old")); err != nil {
		t.Fatal(err)
	}
	appendSpool("newest line\r\n")
	got = sc.capture(m.root, "s", 40, 10)
	if !strings.Contains(got, "newest line") {
		t.Fatalf("newest output missing after second rotation: %q", got)
	}
	if strings.Count(got, "after rotation") > 1 {
		t.Fatalf("content applied twice after rotation: %q", got)
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
