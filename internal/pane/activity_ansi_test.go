package pane

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// TestClassifyIgnoresStyling pins that classification reads visible text, not
// bytes. Panes arrive styled — tmux is captured with -e and the PTY renderer
// emits its own colour — and agents colour phrases word by word, so an SGR
// sequence can land between any two characters of a signal phrase. Every regex
// in this package is written against the plain text, so without stripping, a
// working agent reads as something else entirely.
func TestClassifyIgnoresStyling(t *testing.T) {
	// A dim hint, styled the way an agent actually emits it: colour set before
	// the phrase, changed partway through, never reset.
	styled := strings.Repeat("\n", 12) +
		"\x1b[38;2;117;113;94m(\x1b[38;5;8mesc \x1b[2mto interrupt\x1b[38;2;1;1;1m)"
	res := Classify(Observation{Pane: styled})
	if res.State != domain.AgentStateWorking {
		t.Fatalf("styled interrupt hint must read as working, got %s (%s)", res.State, res.Reason)
	}

	plain := strings.Repeat("\n", 12) + "(esc to interrupt)"
	if plainRes := Classify(Observation{Pane: plain}); plainRes.State != res.State {
		t.Fatalf("styling changed the verdict: plain=%s styled=%s", plainRes.State, res.State)
	}
}

// TestClassifyChangeDetectionComparesVisibleText guards the subtle half of the
// fix: Pane and Previous must be stripped alike. Stripping only one would make
// every sample differ from the last, so every session would look like it was
// producing output forever.
func TestClassifyChangeDetectionComparesVisibleText(t *testing.T) {
	body := strings.Repeat("some settled output\n", 12)
	// Same visible text, different styling — a repaint, not progress.
	prev := body + "\x1b[31mdone\x1b[0m\n"
	now := body + "\x1b[32mdone\x1b[0m\n"

	res := Classify(Observation{Pane: now, Previous: prev, SinceOutput: 10 * 60 * 1e9})
	if res.State == domain.AgentStateWorking && res.Reason == "pane changed since last sample" {
		t.Fatal("a restyled but unchanged pane must not read as working")
	}
}

func TestStripANSI(t *testing.T) {
	cases := map[string]string{
		"\x1b[38;2;1;2;3mred\x1b[0m":  "red",
		"plain":                       "plain",
		"\x1b[2J\x1b[Hcleared":        "cleared",
		"a\x1b[31mb\x1b[0mc":          "abc",
		"\x1b]0;window title\x07here": "here",
	}
	for in, want := range cases {
		if got := StripANSI(in); got != want {
			t.Errorf("StripANSI(%q) = %q, want %q", in, got, want)
		}
	}
}
