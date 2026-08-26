package ptyruntime

import (
	"strings"

	"github.com/hinshun/vt10x"
)

// Default screen geometry when a session reports no size.
const (
	defaultCols = 80
	defaultRows = 24
)

// RenderScreen replays raw PTY output through a terminal emulator and returns
// the resulting screen as plain text.
//
// This is what makes PTY sessions comparable to tmux ones. `tmux capture-pane`
// hands back a *rendered* screen, because tmux is itself a terminal emulator.
// The PTY spool is the opposite: every byte the child ever wrote, including all
// the cursor addressing, clears and partial redraws a full-screen agent emits
// many times a second. Printing that stream verbatim shows overlapping frames
// and stray escape sequences rather than the session — which is exactly what the
// dashboard preview did.
//
// It also matters beyond the preview: pane.Classify was written against
// rendered tmux output, so feeding it the raw stream made its "what is this
// agent doing" signals unreliable for PTY sessions too.
func RenderScreen(spool []byte, cols, rows int) string {
	if cols <= 0 {
		cols = defaultCols
	}
	if rows <= 0 {
		rows = defaultRows
	}

	term := vt10x.New()
	term.Resize(cols, rows)
	// Write takes the terminal's own lock, so it must not be called with the
	// lock already held — doing so deadlocks on a non-reentrant mutex. Errors
	// are immaterial: the emulator applies whatever it understands, and a
	// malformed tail should still render everything before it.
	_, _ = term.Write(spool)

	term.Lock()
	defer term.Unlock()

	var b strings.Builder
	b.Grow(rows * (cols + 1))
	for y := 0; y < rows; y++ {
		line := make([]rune, 0, cols)
		for x := 0; x < cols; x++ {
			ch := term.Cell(x, y).Char
			if ch == 0 {
				ch = ' '
			}
			line = append(line, ch)
		}
		// Trailing blanks are padding, not content, and they defeat the
		// tail-based classification that looks at the last non-empty lines.
		b.WriteString(strings.TrimRight(string(line), " "))
		if y < rows-1 {
			b.WriteByte('\n')
		}
	}
	// Blank rows below the cursor are padding too.
	return strings.TrimRight(b.String(), "\n")
}

// parseSize reads a "<cols>x<rows>" size as reported in the session contract.
// Anything unparseable yields zeroes, which RenderScreen treats as "default".
func parseSize(size string) (cols, rows int) {
	parts := strings.SplitN(strings.TrimSpace(size), "x", 2)
	if len(parts) != 2 {
		return 0, 0
	}
	return atoiSafe(parts[0]), atoiSafe(parts[1])
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
		if n > 1<<16 {
			return 0
		}
	}
	return n
}
