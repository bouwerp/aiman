package ptyruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The whole point: raw PTY output is a byte stream of redraws, not a screen.
// Printing it verbatim (which the preview used to do) shows overlapping frames
// and stray escapes; replaying it through an emulator shows the session.
func TestRenderScreenAppliesCursorAddressing(t *testing.T) {
	// Write "STALE", clear the screen, then paint "FRESH" at the top-left —
	// exactly the shape of a full-screen agent repainting.
	spool := []byte("STALE OUTPUT\r\n\x1b[2J\x1b[H" + "FRESH OUTPUT")

	got := RenderScreen(spool, 40, 6)

	if !strings.Contains(got, "FRESH OUTPUT") {
		t.Fatalf("expected the current frame, got %q", got)
	}
	if strings.Contains(got, "STALE OUTPUT") {
		t.Errorf("cleared content must not survive rendering, got %q", got)
	}
	if strings.Contains(got, "\x1b[") {
		t.Errorf("escape sequences must be applied, not emitted: %q", got)
	}
}

// Overwriting in place is the common case for a spinner or status line.
func TestRenderScreenResolvesInPlaceOverwrite(t *testing.T) {
	spool := []byte("working 1s\r" + "working 9s")
	got := RenderScreen(spool, 40, 3)
	if !strings.Contains(got, "working 9s") {
		t.Fatalf("expected the latest value, got %q", got)
	}
	if strings.Count(got, "working") != 1 {
		t.Errorf("carriage return should overwrite, not append: %q", got)
	}
}

// Trailing padding would otherwise defeat pane.Classify, which reasons about
// the last non-empty lines of a pane.
func TestRenderScreenTrimsPadding(t *testing.T) {
	got := RenderScreen([]byte("hello"), 80, 24)
	if got != "hello" {
		t.Fatalf("expected padding trimmed, got %q", got)
	}
}

func TestRenderScreenHandlesEmptyAndBadSize(t *testing.T) {
	if got := RenderScreen(nil, 0, 0); got != "" {
		t.Errorf("empty spool should render empty, got %q", got)
	}
	if got := RenderScreen([]byte("x"), -5, -5); !strings.Contains(got, "x") {
		t.Errorf("a bad size should fall back to defaults, got %q", got)
	}
}

func TestParseSize(t *testing.T) {
	cases := map[string][2]int{
		"120x40": {120, 40}, "80x24": {80, 24},
		"": {0, 0}, "junk": {0, 0}, "12x": {12, 0}, "x40": {0, 40},
	}
	for in, want := range cases {
		c, r := parseSize(in)
		if c != want[0] || r != want[1] {
			t.Errorf("parseSize(%q) = %d,%d want %d,%d", in, c, r, want[0], want[1])
		}
	}
}

// A leftover directory with no meta and no exit file must be forgettable.
//
// List and Get report such a session (they only need the directory to exist)
// while Forget used to deny it existed, so stale entries could never be
// removed — three of them sat in a live `pty list` indefinitely.
func TestForgetRemovesAGoneSessionDirectory(t *testing.T) {
	root := shortTempDir(t)
	m := NewManagerWithRoot(root, []string{"true"})

	// The state a killed-then-partly-cleaned session leaves: directory only.
	dir := filepath.Join(root, "pty", "stale")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Get("stale"); err != nil {
		t.Fatalf("a leftover directory should still be listed/gettable: %v", err)
	}
	if err := m.Forget("stale"); err != nil {
		t.Fatalf("Forget should remove it, got %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("directory should be gone, stat err = %v", err)
	}
	// And a genuinely absent session is still not found.
	if err := m.Forget("never-existed"); err == nil {
		t.Error("expected ErrNotFound for a session with no directory")
	}
}
