package server

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/hinshun/vt10x"
)

func TestFollowOutputKeepsShellBytes(t *testing.T) {
	chunk := []byte("prompt$ ls\r\n")
	if got := followOutput(chunk, "old line", nil, "view", 4); !bytes.Equal(got, chunk) {
		t.Fatalf("no redraw should forward the chunk: %q", got)
	}
	if got := followOutput(chunk, "", []string{"old line"}, "view", 4); !bytes.Equal(got, chunk) {
		t.Fatalf("empty suffix should forward the chunk: %q", got)
	}
	got := followOutput(chunk, "old line", []string{"old line"}, "AA\nBB", 2)
	if bytes.Equal(got, chunk) {
		t.Fatal("a redrawn line should replace the raw chunk")
	}
	if !strings.Contains(string(got), "old line\x1b[K\r\n") {
		t.Fatalf("paint = %q", got)
	}
}

func TestHistorySuffix(t *testing.T) {
	if got := historySuffix("A\nB", "A\nB\nC"); got != "C" {
		t.Fatalf("suffix: %q", got)
	}
	if got := historySuffix("A\nB", "A\nB"); got != "" {
		t.Fatalf("unchanged history should not repaint: %q", got)
	}
	if got := historySuffix("A\nB", "Z"); got != "" {
		t.Fatalf("a rewritten history is not a suffix: %q", got)
	}
	if got := historySuffix("", "A"); got != "A" {
		t.Fatalf("first history: %q", got)
	}
}

// Attach used to print every history line as a newline. A terminal paints
// each of those as it arrives, so the session scrolls past from the first
// line. The seed has to be one synchronized update: the wheel still reaches
// the history, and the screen that gets painted is the live one.
func TestInlineHistoryPaintDoesNotReplayHistory(t *testing.T) {
	var hist strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&hist, "H%02d\n", i)
	}
	paint := inlineHistoryPaint(strings.TrimRight(hist.String(), "\n"), "AA\nBB\nCC\nDD", 4)
	got := string(paint)
	if strings.Contains(got, "\x1b[?1049h") {
		t.Fatalf("inline paint must stay on the primary screen: %q", got)
	}
	open := strings.Index(got, "\x1b[?2026h")
	closeAt := strings.LastIndex(got, "\x1b[?2026l")
	if open != 0 || closeAt < 0 || closeAt+len("\x1b[?2026l") != len(got) {
		t.Fatalf("history seed must be one synchronized update: %q", got)
	}
	outside := got[closeAt+len("\x1b[?2026l"):]
	if strings.Contains(outside, "\n") {
		t.Fatalf("newline outside the synchronized update: %q", outside)
	}

	term := vt10x.New(vt10x.WithSize(20, 4))
	var scrolled []string
	vt10x.SetScrollOff(term, func(line []vt10x.Glyph) {
		var b strings.Builder
		for _, g := range line {
			if g.Char != 0 && g.Char != ' ' {
				b.WriteRune(g.Char)
			}
		}
		if b.Len() > 0 {
			scrolled = append(scrolled, b.String())
		}
	})
	// The cursor sits on the last row after the attach notice. Chunks arrive
	// as separate reads. None of them may leave the first history line on screen.
	_, _ = term.Write([]byte("\n\n\n"))
	for i := 0; i < len(paint); i += 7 {
		end := i + 7
		if end > len(paint) {
			end = len(paint)
		}
		if _, err := term.Write(paint[i:end]); err != nil {
			t.Fatalf("write: %v", err)
		}
		if end < len(paint) && rowText(term, 0) == "H00" {
			t.Fatal("attach showed the start of history before the live screen")
		}
	}
	for y, row := range []string{"AA", "BB", "CC", "DD"} {
		if got := rowText(term, y); got != row {
			t.Fatalf("row %d = %q, want %q", y, got, row)
		}
	}
	if len(scrolled) == 0 || scrolled[0] != "H00" {
		t.Fatalf("scrollback = %q", scrolled)
	}
}

func rowText(term vt10x.Terminal, y int) string {
	var b strings.Builder
	for x := 0; x < 20; x++ {
		ch := term.Cell(x, y).Char
		if ch == 0 {
			ch = ' '
		}
		b.WriteRune(ch)
	}
	return strings.TrimRight(b.String(), " ")
}

func TestInlineHistoryPaintPushesLineAndKeepsViewport(t *testing.T) {
	term := vt10x.New(vt10x.WithSize(20, 4))
	var scrolled []string
	vt10x.SetScrollOff(term, func(line []vt10x.Glyph) {
		var b strings.Builder
		for _, g := range line {
			if g.Char != 0 && g.Char != ' ' {
				b.WriteRune(g.Char)
			}
		}
		if b.Len() > 0 {
			scrolled = append(scrolled, b.String())
		}
	})
	_, _ = term.Write(inlineHistoryPaint("OLD LINE", "AA\nBB\nCC\nDD", 4))
	if len(scrolled) != 1 || scrolled[0] != "OLDLINE" {
		t.Fatalf("scrollback = %q", scrolled)
	}
	want := []string{"AA", "BB", "CC", "DD"}
	for y, row := range want {
		var b strings.Builder
		for x := 0; x < 20; x++ {
			ch := term.Cell(x, y).Char
			if ch == 0 {
				ch = ' '
			}
			b.WriteRune(ch)
		}
		if got := strings.TrimRight(b.String(), " "); got != row {
			t.Fatalf("row %d = %q, want %q", y, got, row)
		}
	}
}
