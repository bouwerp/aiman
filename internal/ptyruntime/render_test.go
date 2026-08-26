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
	// Cursor addressing is applied rather than passed through. Colour is the
	// exception and is re-emitted (see the colour tests below); this input
	// carries none, so nothing at all should come back.
	if strings.Contains(got, "\x1b[") {
		t.Errorf("cursor addressing must be applied, not emitted: %q", got)
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

// Colour is the one thing the renderer re-emits. A preview without it is much
// harder to read, and tmux panes (captured with -e) have always had it, so a
// PTY session showing plain grey text looked broken by comparison.
func TestRenderScreenEmitsColour(t *testing.T) {
	// 24-bit foreground, then a 256-palette foreground, then back to default.
	spool := []byte("\x1b[38;2;255;100;50mwarm\x1b[38;5;33mblue\x1b[0mplain")
	got := RenderScreen(spool, 40, 3)

	if !strings.Contains(got, "38;2;255;100;50") {
		t.Errorf("24-bit foreground lost: %q", got)
	}
	if !strings.Contains(got, "38;5;33") {
		t.Errorf("palette foreground lost: %q", got)
	}
	if !strings.Contains(ansiStrip(got), "warmblueplain") {
		t.Errorf("visible text must survive: %q", ansiStrip(got))
	}
}

// Backgrounds matter as much as foregrounds: vt10x resolves reverse video by
// swapping the pair when it stores the cell, so dropping the background would
// render reversed text as dark-on-dark.
func TestRenderScreenEmitsBackgroundAndReverse(t *testing.T) {
	if got := RenderScreen([]byte("\x1b[48;5;4mon blue"), 40, 3); !strings.Contains(got, "48;5;4") {
		t.Errorf("background lost: %q", got)
	}
	// Reverse video with an explicit pair: the stored cell has them swapped, so
	// both channels must be present rather than the text going invisible.
	got := RenderScreen([]byte("\x1b[31;47m\x1b[7mreversed"), 40, 3)
	if !strings.Contains(got, "38;") || !strings.Contains(got, "48;") {
		t.Errorf("reverse video must keep both channels set: %q", got)
	}
}

// Every styled row closes its own styling. An unterminated run bleeds into the
// next line and into the surrounding UI when the screen is shown in a panel.
func TestRenderScreenSealsEachRow(t *testing.T) {
	spool := []byte("\x1b[31mred line one\r\n\x1b[32mgreen line two\r\nplain three")
	got := RenderScreen(spool, 40, 5)
	for i, line := range strings.Split(got, "\n") {
		if !strings.Contains(line, "\x1b[") {
			continue
		}
		if !strings.HasSuffix(line, "\x1b[0m") {
			t.Errorf("line %d leaves styling open: %q", i, line)
		}
	}
}

// A row that ends back at the default colours needs no trailing reset. Going
// back to default is emitted as the explicit pair 39;49 rather than a full
// reset, so the row is already closed by the time it ends.
func TestRenderScreenOmitsRedundantReset(t *testing.T) {
	got := RenderScreen([]byte("\x1b[31mred\x1b[0m then plain"), 40, 3)
	if !strings.Contains(got, "39;49") {
		t.Errorf("returning to default colours should be emitted: %q", got)
	}
	if strings.HasSuffix(got, "\x1b[0m") {
		t.Errorf("row already at default colours must not get a trailing reset: %q", got)
	}
	if !strings.Contains(ansiStrip(got), "red then plain") {
		t.Errorf("visible text must survive: %q", ansiStrip(got))
	}
}

func TestRenderScreenPlainStaysPlain(t *testing.T) {
	if got := RenderScreen([]byte("no colour here"), 40, 3); strings.Contains(got, "\x1b") {
		t.Errorf("uncoloured content must render without escapes: %q", got)
	}
}

// A blank cell carrying a background is visible, so it counts as content; a
// truly empty row still renders empty.
func TestRenderScreenKeepsColouredBlanks(t *testing.T) {
	got := RenderScreen([]byte("\x1b[48;5;1m   \x1b[0m"), 40, 3)
	if !strings.Contains(got, "48;5;1") {
		t.Errorf("a coloured run of blanks is visible and must be kept: %q", got)
	}
	if got := RenderScreen([]byte("\r\n\r\n"), 40, 3); got != "" {
		t.Errorf("blank rows must render empty, got %q", got)
	}
}

func TestColorParamForms(t *testing.T) {
	cases := []struct{ in, wantFG string }{
		{"\x1b[38;5;0m0", "38;5;0"},         // index 0 is black, not "reset"
		{"\x1b[38;2;0;0;0m0", "38;5;0"},     // rgb(0,0,0) packs to 0, indistinguishable from index 0
		{"\x1b[38;2;1;2;3mx", "38;2;1;2;3"}, // a real 24-bit value round-trips
	}
	for _, c := range cases {
		got := RenderScreen([]byte(c.in), 20, 2)
		if !strings.Contains(got, c.wantFG) {
			t.Errorf("RenderScreen(%q) = %q, want it to contain %q", c.in, got, c.wantFG)
		}
	}
}

// ansiStrip is a local helper so these tests do not depend on a strip
// implementation living in another package.
func ansiStrip(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
