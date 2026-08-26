package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestSealPreviewLinesClosesOpenStyling(t *testing.T) {
	// The shape a real tmux capture has: foreground and background set per
	// line, never reset. 53 of 54 lines looked like this in the capture that
	// prompted the fix.
	line := "\x1b[38;2;255;255;255m\x1b[48;2;10;10;10m  content here   "
	got := sealPreviewLines(line)
	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Fatalf("open styling must be sealed, got %q", got)
	}
	if !styleOpenAtEnd(line) {
		t.Fatal("test premise wrong: the input should leave styling open")
	}
	if styleOpenAtEnd(got) {
		t.Fatal("sealed line must not leave styling open")
	}
}

func TestSealPreviewLinesPreservesVisibleContent(t *testing.T) {
	in := "\x1b[31mred\x1b[48;5;4m bg\n\x1b[32mgreen\x1b[0m\nplain text\n"
	out := sealPreviewLines(in)

	inLines, outLines := strings.Split(in, "\n"), strings.Split(out, "\n")
	if len(inLines) != len(outLines) {
		t.Fatalf("line count changed: %d -> %d", len(inLines), len(outLines))
	}
	for i := range inLines {
		if ansi.Strip(inLines[i]) != ansi.Strip(outLines[i]) {
			t.Errorf("line %d visible text changed: %q -> %q", i, inLines[i], outLines[i])
		}
		if ansi.StringWidth(inLines[i]) != ansi.StringWidth(outLines[i]) {
			t.Errorf("line %d visible width changed", i)
		}
	}
}

func TestSealPreviewLinesLeavesClosedLinesAlone(t *testing.T) {
	for _, in := range []string{
		"plain, no escapes at all",
		"\x1b[31mred\x1b[0m",
		"\x1b[31mred\x1b[m",      // bare CSI m is a full reset
		"\x1b[38;5;9mfg\x1b[39m", // channel returned to default
		"\x1b[31m\x1b[41mboth\x1b[0m",
	} {
		if got := sealPreviewLines(in); got != in {
			t.Errorf("sealPreviewLines(%q) = %q, want unchanged", in, got)
		}
	}
}

// TestStyleOpenAtEndTracksChannelsSeparately covers the trap that a per-line
// boolean falls into: 39 and 49 each reset one channel only, so a foreground
// still set after a 49 must keep the line open.
func TestStyleOpenAtEndTracksChannelsSeparately(t *testing.T) {
	cases := map[string]bool{
		"\x1b[38;5;1mfg then bg-default\x1b[49m": true, // fg survives 49
		"\x1b[48;5;1mbg then fg-default\x1b[39m": true, // bg survives 39
		"\x1b[38;5;1m\x1b[39mboth cleared":       false,
		"\x1b[1mbold\x1b[22m":                    false, // attribute turned off
		"\x1b[1mbold left on":                    true,
	}
	for in, want := range cases {
		if got := styleOpenAtEnd(in); got != want {
			t.Errorf("styleOpenAtEnd(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestStyleOpenAtEndConsumesExtendedColourParams is the parsing trap: in
// "38;5;0" the 0 is a palette index, not a reset, and in "38;5;1" the 5 and 1
// are not blink and bold. Reading them as standalone parameters gets both the
// style state and the reset decision wrong.
func TestStyleOpenAtEndConsumesExtendedColourParams(t *testing.T) {
	cases := map[string]bool{
		"\x1b[38;5;0mblack fg by index":   true, // the 0 must not read as reset
		"\x1b[48;5;0mblack bg by index":   true,
		"\x1b[38;2;0;0;0mblack fg by rgb": true,
		"\x1b[38;5;1mindexed":             true,
		"\x1b[0;38;5;2mreset then set":    true,
		"\x1b[38;5;2;0mset then reset":    false,
	}
	for in, want := range cases {
		if got := styleOpenAtEnd(in); got != want {
			t.Errorf("styleOpenAtEnd(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestStyleOpenAtEndIgnoresNonSGR makes sure cursor moves and similar are not
// mistaken for styling.
func TestStyleOpenAtEndIgnoresNonSGR(t *testing.T) {
	for _, in := range []string{"\x1b[2J\x1b[Hcleared", "\x1b[10;5Hpositioned", "text\x1b[K"} {
		if styleOpenAtEnd(in) {
			t.Errorf("styleOpenAtEnd(%q) must be false: no SGR present", in)
		}
	}
	// A truncated sequence at end of line must not panic or claim styling.
	for _, in := range []string{"text\x1b[", "text\x1b", "text\x1b[38;5"} {
		_ = styleOpenAtEnd(in)
	}
}

func TestWidestLineIgnoresStyling(t *testing.T) {
	// Same visible text, one styled: the width must not count escape bytes.
	if got, want := widestLine("\x1b[38;2;1;2;3mabcde\x1b[0m"), 5; got != want {
		t.Errorf("widestLine = %d, want %d", got, want)
	}
	if got, want := widestLine("ab\nabcd\na"), 4; got != want {
		t.Errorf("widestLine = %d, want %d", got, want)
	}
}

// The hint only appears when the session really is wider than the panel, since
// that is the only time the right edge is being cut.
func TestPreviewPanHintOnlyWhenContentOverflows(t *testing.T) {
	m := &Model{}
	m.viewport.SetWidth(80)

	m.previewCols = 80
	if got := m.previewPanHint(); got != "" {
		t.Errorf("no hint expected when content fits, got %q", got)
	}

	m.previewCols = 273
	hint := m.previewPanHint()
	if !strings.Contains(hint, "273") || !strings.Contains(hint, "80") {
		t.Errorf("hint should report both widths, got %q", hint)
	}
}

// setPreviewContent must seal what it displays while leaving the raw capture
// intact — the raw form is what change detection compares and what Y yanks.
func TestSetPreviewContentSealsWithoutMutatingRaw(t *testing.T) {
	raw := "\x1b[38;2;255;255;255m\x1b[48;2;10;10;10mopen styling"
	m := &Model{tmuxOutput: raw}
	m.viewport.SetWidth(120)
	m.viewport.SetHeight(10)
	m.setPreviewContent()

	if m.tmuxOutput != raw {
		t.Errorf("raw capture must not be rewritten: %q", m.tmuxOutput)
	}
	if m.previewCols != ansi.StringWidth(raw) {
		t.Errorf("previewCols = %d, want %d", m.previewCols, ansi.StringWidth(raw))
	}
	if styleOpenAtEnd(sealPreviewLines(m.tmuxOutput)) {
		t.Error("displayed content must be sealed")
	}
}
