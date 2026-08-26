package main

import (
	"strings"
	"testing"
)

// TestPTYKeySequences pins the actual bytes. The whole reason --key exists is
// that a shell does not interpret the escape in `--data "\r"` or
// `--data '\x03'`: those pass the literal characters through, so the agent typed
// "\r" into its input box instead of submitting, and "\x03" instead of being
// interrupted.
func TestPTYKeySequences(t *testing.T) {
	cases := map[string]string{
		"enter":  "\r",
		"return": "\r",
		"ctrl-c": "\x03",
		"ctrl-d": "\x04",
		"esc":    "\x1b",
		"tab":    "\t",
	}
	for name, want := range cases {
		got, ok := ptyKeySequence(name)
		if !ok {
			t.Errorf("%q should be a known key", name)
			continue
		}
		if got != want {
			t.Errorf("%q = %q, want %q", name, got, want)
		}
		if len(got) != 1 {
			t.Errorf("%q produced %d bytes; a key must be the control byte itself, not its escape spelling", name, len(got))
		}
	}
}

func TestPTYKeySequenceIsForgivingAboutCase(t *testing.T) {
	for _, name := range []string{"ENTER", " enter ", "Ctrl-C"} {
		if _, ok := ptyKeySequence(name); !ok {
			t.Errorf("%q should resolve", name)
		}
	}
}

func TestPTYKeySequenceRejectsUnknown(t *testing.T) {
	for _, name := range []string{"", "ctrl+c", "\\r", "meta-x"} {
		if seq, ok := ptyKeySequence(name); ok {
			t.Errorf("%q should be unknown, got %q", name, seq)
		}
	}
	// The error message has to tell the caller what is available.
	names := ptyKeyNames()
	for _, want := range []string{"enter", "ctrl-c"} {
		if !strings.Contains(names, want) {
			t.Errorf("key list missing %q: %s", want, names)
		}
	}
}
