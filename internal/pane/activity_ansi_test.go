package pane

import (
	"strings"
	"testing"
	"time"

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

// A terminal title that changed a moment ago is the one signal here that is not
// an inference from rendered text: agents animate a spinner in the title while
// they work, so a moving title means work.
func TestClassifyUsesTitleActivity(t *testing.T) {
	// A pane with no useful markers at all, plus a long silence, would otherwise
	// read as idle.
	quiet := strings.Repeat("some settled output\n", 12)

	idle := Classify(Observation{Pane: quiet, SinceOutput: 10 * time.Minute, SinceTitleChange: -1})
	if idle.State != domain.AgentStateIdle {
		t.Fatalf("premise: expected idle without the title signal, got %s (%s)", idle.State, idle.Reason)
	}

	working := Classify(Observation{Pane: quiet, SinceOutput: 10 * time.Minute, SinceTitleChange: time.Second})
	if working.State != domain.AgentStateWorking {
		t.Errorf("a title that changed a second ago means working, got %s (%s)", working.State, working.Reason)
	}
	if working.Confidence != High {
		t.Errorf("the title signal is direct evidence, expected high confidence")
	}

	// A stale title must not keep a finished session looking busy.
	stale := Classify(Observation{Pane: quiet, SinceOutput: 10 * time.Minute,
		SinceTitleChange: TitleActivityWindow + time.Second})
	if stale.State == domain.AgentStateWorking {
		t.Errorf("a title older than the window is not evidence of work: %s", stale.Reason)
	}
}

// The zero value has to mean "no information". Every caller that does not set
// the field would otherwise look permanently busy.
func TestClassifyTreatsUnsetTitleAgeAsUnknown(t *testing.T) {
	quiet := strings.Repeat("some settled output\n", 12)
	res := Classify(Observation{Pane: quiet, SinceOutput: 10 * time.Minute})
	if res.State == domain.AgentStateWorking {
		t.Fatalf("an unset title age must not read as a title that just changed: %s", res.Reason)
	}
}

// A question outranks a moving title: an agent that stopped to ask has stopped
// working, and its last title may only be a second old.
func TestClassifyPrefersAQuestionOverAMovingTitle(t *testing.T) {
	asking := "Do you want to proceed?\n[y/N] "
	res := Classify(Observation{Pane: asking, SinceOutput: time.Second, SinceTitleChange: time.Second})
	if res.State != domain.AgentStateWaitingInput {
		t.Errorf("expected waiting_input, got %s (%s)", res.State, res.Reason)
	}
}
