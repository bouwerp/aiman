package server

import (
	"bytes"
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
