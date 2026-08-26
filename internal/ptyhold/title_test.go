package ptyhold

import (
	"strings"
	"testing"
)

func TestTitleScannerExtractsTitles(t *testing.T) {
	var s titleScanner
	got := s.Feed([]byte("before\x1b]0;first title\x07after\x1b]2;second\x1b\\tail"))
	want := []string{"first title", "second"}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("title %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// A PTY read can end anywhere, so a title split across reads must still be seen
// exactly once.
func TestTitleScannerHandlesSplitsAtEveryByte(t *testing.T) {
	const full = "noise\x1b]0;spinner task\x07more noise"
	for cut := 0; cut <= len(full); cut++ {
		var s titleScanner
		var titles []string
		titles = append(titles, s.Feed([]byte(full[:cut]))...)
		titles = append(titles, s.Feed([]byte(full[cut:]))...)
		if len(titles) != 1 || titles[0] != "spinner task" {
			t.Fatalf("split at %d: got %q", cut, titles)
		}
	}
}

// Other OSC numbers are not titles. Hyperlinks (OSC 8) appear in real agent
// output and must not be mistaken for one.
func TestTitleScannerIgnoresOtherOSC(t *testing.T) {
	var s titleScanner
	got := s.Feed([]byte("\x1b]8;id=1;https://example.com/pr/1\x07link text\x1b]8;;\x07"))
	if len(got) != 0 {
		t.Fatalf("expected no titles, got %q", got)
	}
}

func TestTitleScannerSurvivesUnterminatedSequences(t *testing.T) {
	var s titleScanner
	// An OSC that never terminates must not be reported and must not be held
	// forever.
	if got := s.Feed([]byte("\x1b]0;" + strings.Repeat("x", titleScanMax+50))); len(got) != 0 {
		t.Fatalf("expected nothing from an unterminated sequence, got %q", got)
	}
	if len(s.pending) != 0 {
		t.Errorf("an over-long partial sequence must be dropped, holding %d bytes", len(s.pending))
	}
	// The scanner still works afterwards.
	if got := s.Feed([]byte("\x1b]0;recovered\x07")); len(got) != 1 || got[0] != "recovered" {
		t.Fatalf("scanner did not recover: %q", got)
	}
}

// The real shape, from a captured session: the glyph changes while the task text
// stays put, which is what makes a title change a working signal.
func TestTitleScannerReadsRealAgentTitles(t *testing.T) {
	var s titleScanner
	stream := "\x1b]0;◐ Read .aiman_task.md\x07output\x1b]0;◑ Read .aiman_task.md\x07"
	got := s.Feed([]byte(stream))
	if len(got) != 2 {
		t.Fatalf("expected 2 titles, got %q", got)
	}
	if got[0] == got[1] {
		t.Errorf("the spinner glyph should differ between frames: %q", got)
	}
	for _, title := range got {
		if !strings.Contains(title, "Read .aiman_task.md") {
			t.Errorf("task text lost: %q", title)
		}
	}
}

func TestTitleScannerEmptyAndPlainInput(t *testing.T) {
	var s titleScanner
	if got := s.Feed(nil); len(got) != 0 {
		t.Errorf("nil input: %q", got)
	}
	if got := s.Feed([]byte("just some output\r\n")); len(got) != 0 {
		t.Errorf("plain output: %q", got)
	}
	// A trailing lone ESC is a possible split point, not a title.
	if got := s.Feed([]byte("text\x1b")); len(got) != 0 {
		t.Errorf("trailing ESC: %q", got)
	}
	if got := s.Feed([]byte("]0;after split\x07")); len(got) != 1 || got[0] != "after split" {
		t.Errorf("did not resume across a lone trailing ESC: %q", got)
	}
}
