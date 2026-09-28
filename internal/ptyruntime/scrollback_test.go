package ptyruntime

import (
	"fmt"
	"strings"
	"testing"
)

// PTY capture used to render only the session's current screen, while a tmux
// session captures its full scrollback history (`capture-pane -S -`). A
// dashboard preview or `session read` on a PTY session therefore had nothing
// to scroll back to. The journal below records each line as it scrolls off
// the native-size grid — including lines scrolled inside an agent's scroll
// region, which no taller replay could recover: cursor-addressing agents lay
// out against the session's real size, so a wrong-height replay smears
// repaints down the grid instead of converging on what was shown.
func TestJournalRetainsScrolledLines(t *testing.T) {
	m, appendSpool := screenFixture(t)
	for i := 1; i <= 30; i++ {
		appendSpool(fmt.Sprintf("line %03d\r\n", i))
		if _, err := m.CaptureScreen("s"); err != nil {
			t.Fatalf("CaptureScreen: %v", err)
		}
	}

	got, err := m.CaptureScrollback("s", 0)
	if err != nil {
		t.Fatalf("CaptureScrollback: %v", err)
	}
	for _, want := range []string{"line 001", "line 015", "line 030"} {
		if !strings.Contains(got, want) {
			t.Errorf("scrollback should contain %q: %q", want, got)
		}
	}
}

// The journal must hold each exited line once, oldest first: history followed
// by the live screen, with no gaps and no repeats.
func TestJournalOrdersHistoryBeforeScreen(t *testing.T) {
	m, appendSpool := screenFixture(t)
	for i := 1; i <= 15; i++ {
		appendSpool(fmt.Sprintf("line %03d\r\n", i))
		if _, err := m.CaptureScreen("s"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := m.CaptureScrollback("s", 0)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(got, "\n")
	var nums []string
	for _, l := range lines {
		if strings.HasPrefix(l, "line ") {
			nums = append(nums, strings.TrimPrefix(l, "line "))
		}
	}
	if len(nums) != 15 {
		t.Fatalf("want 15 transcript lines in order, got %d: %q", len(nums), got)
	}
	for i, n := range nums {
		if want := fmt.Sprintf("%03d", i+1); n != want {
			t.Fatalf("line %d: want %q got %q (full: %q)", i, want, n, got)
		}
	}
}

// An in-place repaint moves the cursor and rewrites rows without scrolling:
// nothing left the grid, so nothing is journaled.
func TestJournalIgnoresRepaints(t *testing.T) {
	m, appendSpool := screenFixture(t)
	sc := m.screenFor("s")
	appendSpool("first\r\nsecond\r\n")
	sc.capture(m.root, "s", 40, 10)
	appendSpool("\x1b[1;1HREPAINTED\r\n\x1b[2;1Hsecond\r\n")
	sc.capture(m.root, "s", 40, 10)

	got, err := m.CaptureScrollback("s", 0)
	if err != nil {
		t.Fatal(err)
	}
	if c := strings.Count(got, "second"); c != 1 {
		t.Errorf("repainted line should appear once, got %d in %q", c, got)
	}
	if strings.Contains(got, "first") {
		t.Errorf("overwritten line should not be journaled: %q", got)
	}
}

// A clear wipes the grid without scrolling: cleared lines were not scrolled
// and must not be journaled.
func TestJournalIgnoresClear(t *testing.T) {
	m, appendSpool := screenFixture(t)
	sc := m.screenFor("s")
	appendSpool("before clear\r\n")
	sc.capture(m.root, "s", 40, 10)
	appendSpool("\x1b[2J\x1b[Hafter clear\r\n")
	sc.capture(m.root, "s", 40, 10)

	got, err := m.CaptureScrollback("s", 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "before clear") {
		t.Errorf("cleared line should not be journaled: %q", got)
	}
	if !strings.Contains(got, "after clear") {
		t.Errorf("post-clear line missing: %q", got)
	}
}

// Lines scrolled inside a scroll region leave the grid the same way
// full-screen scrolls do: the region's top lines must be journaled even
// though the rows outside the region never moved.
func TestJournalCapturesRegionScroll(t *testing.T) {
	m, appendSpool := screenFixture(t)
	sc := m.screenFor("s")
	appendSpool("\x1b[2;9r") // scroll region rows 2-9 of a 10-row grid
	appendSpool("\x1b[1;1H") // the region set homes the cursor; put it back
	appendSpool("top chrome\r\n")
	appendSpool("\x1b[2;1H") // region top, where the list starts
	for i := 1; i <= 12; i++ {
		appendSpool(fmt.Sprintf("item %03d\r\n", i))
		sc.capture(m.root, "s", 40, 10)
	}

	got, err := m.CaptureScrollback("s", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"item 001", "item 006", "item 012", "top chrome"} {
		if !strings.Contains(got, want) {
			t.Errorf("scrollback should contain %q: %q", want, got)
		}
	}
}

func TestCaptureScrollbackTailsToLines(t *testing.T) {
	m, appendSpool := screenFixture(t)
	for i := 1; i <= 20; i++ {
		appendSpool(fmt.Sprintf("line %03d\r\n", i))
		if _, err := m.CaptureScreen("s"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := m.CaptureScrollback("s", 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"line 016", "line 017", "line 018", "line 019", "line 020"} {
		if !strings.Contains(got, want) {
			t.Errorf("tailed scrollback should contain %q: %q", want, got)
		}
	}
	if strings.Contains(got, "line 015") {
		t.Errorf("tailed scrollback should stop at 5 lines: %q", got)
	}
}

// A rebuild replays retained bytes to re-establish the picture, not to
// produce output: it must neither journal the restored screen nor drop the
// history already kept.
func TestRebuildKeepsJournalWithoutDuplicates(t *testing.T) {
	m, appendSpool := screenFixture(t)
	sc := m.screenFor("s")
	for i := 1; i <= 15; i++ {
		appendSpool(fmt.Sprintf("line %03d\r\n", i))
		sc.capture(m.root, "s", 40, 10)
	}
	before, err := m.CaptureScrollback("s", 0)
	if err != nil {
		t.Fatal(err)
	}
	// Same row count, new width: the replay re-establishes the picture, so
	// the short lines land identically and only the rebuild path is tested.
	sc.capture(m.root, "s", 100, 10) // resize rebuilds
	after, err := m.CaptureScrollback("s", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"line 001", "line 015"} {
		if !strings.Contains(after, want) {
			t.Errorf("rebuild should keep %q: %q", want, after)
		}
	}
	for _, want := range []string{"line 001", "line 015"} {
		if c := strings.Count(after, want); c != strings.Count(before, want) {
			t.Errorf("rebuild changed %q count %d -> %d: %q", want, strings.Count(before, want), c, after)
		}
	}
}

// A scroll buried in the same segment as a later repaint must still be
// journaled: the feed is split so the scroll and its repaint land in
// different observed windows.
func TestJournalSeparatesScrollFromLaterRepaint(t *testing.T) {
	m, appendSpool := screenFixture(t)
	sc := m.screenFor("s")
	// Fill the 10-row grid without scrolling: vt10x scrolls eagerly, so the
	// Nth newline in N rows already exits a line — and the rebuild feed that
	// establishes the grid journals nothing by design.
	var pre strings.Builder
	for i := 1; i <= 9; i++ {
		fmt.Fprintf(&pre, "S%02d\r\n", i)
	}
	appendSpool(pre.String())
	sc.capture(m.root, "s", 120, 10)

	// One segment: long lines (over the observation window) pushing the S
	// lines off, then a repaint of the top row.
	var seg strings.Builder
	for i := 1; i <= 15; i++ {
		fmt.Fprintf(&seg, "A%02d%s\r\n", i, strings.Repeat("x", 90))
	}
	seg.WriteString("\x1b[1;1HREPAINTED\x1b[K\r\n")
	if len(seg.String()) <= journalPieceBytes {
		t.Fatalf("segment must exceed one observation window, got %d", len(seg.String()))
	}
	appendSpool(seg.String())
	sc.capture(m.root, "s", 120, 10)

	got, err := m.CaptureScrollback("s", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"S01", "S05", "S09", "A01", "REPAINTED"} {
		if !strings.Contains(got, want) {
			t.Errorf("scrollback should contain %q: %q", want, got)
		}
	}
}

// A burst of many screens' worth of lines in a single segment must still be
// journaled in full: the feed is split into observable pieces, so no window
// ever holds more scrolls than the grid can show leaving.
func TestJournalKeepsBurstScrolls(t *testing.T) {
	m, appendSpool := screenFixture(t)
	sc := m.screenFor("s")
	sc.capture(m.root, "s", 120, 10) // establish the grid; nothing fed yet
	var burst strings.Builder
	for i := 1; i <= 50; i++ {
		fmt.Fprintf(&burst, "burst %03d\r\n", i)
	}
	appendSpool(burst.String())
	sc.capture(m.root, "s", 120, 10) // one segment, five screens of scroll

	got, err := m.CaptureScrollback("s", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"burst 001", "burst 025", "burst 050"} {
		if !strings.Contains(got, want) {
			t.Errorf("scrollback should contain %q: %q", want, got)
		}
	}
}

func TestDropScreenForgetsJournal(t *testing.T) {
	m, appendSpool := screenFixture(t)
	// More lines than the default 24-row screen: the earliest end up
	// journaled, not on the grid, so only the journal can produce them.
	for i := 1; i <= 30; i++ {
		appendSpool(fmt.Sprintf("line %03d\r\n", i))
		if _, err := m.CaptureScreen("s"); err != nil {
			t.Fatal(err)
		}
	}
	before, err := m.CaptureScrollback("s", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(before, "line 001") {
		t.Fatalf("want line 001 journaled before the drop: %q", before)
	}
	m.dropScreen("s")
	got, err := m.CaptureScrollback("s", 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "line 001") {
		t.Errorf("dropped session should not retain journaled lines: %q", got)
	}
	if !strings.Contains(got, "line 030") {
		t.Errorf("replayed screen should still reach the latest line: %q", got)
	}
}
