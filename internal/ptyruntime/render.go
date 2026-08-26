package ptyruntime

import (
	"strconv"
	"strings"

	"github.com/hinshun/vt10x"
)

// Default screen geometry when a session reports no size.
const (
	defaultCols = 80
	defaultRows = 24
)

// RenderScreen replays raw PTY output through a terminal emulator and returns
// the resulting screen as text with SGR colour, each line's styling terminated.
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

	term := newTerminal(cols, rows)
	// Write takes the terminal's own lock, so it must not be called with the
	// lock already held — doing so deadlocks on a non-reentrant mutex. Errors
	// are immaterial: the emulator applies whatever it understands, and a
	// malformed tail should still render everything before it.
	_, _ = term.Write(spool)
	return renderTerminal(term, cols, rows)
}

// newTerminal returns an emulator sized for a session.
func newTerminal(cols, rows int) vt10x.Terminal {
	term := vt10x.New()
	term.Resize(cols, rows)
	return term
}

// renderTerminal turns an emulator's current screen into text with colour.
//
// Separate from RenderScreen so a long-lived emulator can be rendered without
// replaying its whole history: the runtime keeps one per session and feeds it
// only the bytes that arrived since the last capture.
func renderTerminal(term vt10x.Terminal, cols, rows int) string {
	term.Lock()
	defer term.Unlock()

	var b strings.Builder
	b.Grow(rows * (cols + 1))
	for y := 0; y < rows; y++ {
		b.WriteString(renderRow(term, y, cols))
		if y < rows-1 {
			b.WriteByte('\n')
		}
	}
	// Blank rows below the cursor are padding too.
	return strings.TrimRight(b.String(), "\n")
}

// renderRow emits one screen row as text plus SGR colour.
//
// Colour is what makes a preview readable — an agent's diffs, warnings and dim
// chrome all carry meaning — and it keeps PTY sessions on a par with tmux ones,
// whose panes are captured with `capture-pane -e`. Attributes are read straight
// off the glyph because vt10x has already resolved them: it brightens bold
// foregrounds and swaps the pair for reverse video when it stores the cell, so
// the colours here are the ones meant to be seen.
func renderRow(term vt10x.Terminal, y, cols int) string {
	last := lastSignificantCol(term, y, cols)
	if last < 0 {
		return ""
	}
	var b strings.Builder
	fg, bg := vt10x.DefaultFG, vt10x.DefaultBG
	for x := 0; x <= last; x++ {
		cell := term.Cell(x, y)
		if cell.FG != fg || cell.BG != bg {
			fg, bg = cell.FG, cell.BG
			b.WriteString("\x1b[" + fgParam(fg) + ";" + bgParam(bg) + "m")
		}
		ch := cell.Char
		if ch == 0 {
			ch = ' '
		}
		b.WriteRune(ch)
	}
	// Terminate the row's styling. An unterminated run leaks the colour into
	// whatever is drawn next — the rest of the row, the following line, or the
	// surrounding UI when the screen is embedded in a panel.
	if fg != vt10x.DefaultFG || bg != vt10x.DefaultBG {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// lastSignificantCol is the rightmost column worth emitting, or -1 for a row
// with nothing on it. Trailing blanks are padding, not content, and they defeat
// the tail-based classification that looks at the last non-empty lines — but a
// blank carrying a background colour is visible, so it counts as content.
func lastSignificantCol(term vt10x.Terminal, y, cols int) int {
	for x := cols - 1; x >= 0; x-- {
		cell := term.Cell(x, y)
		if cell.Char != 0 && cell.Char != ' ' {
			return x
		}
		if cell.BG != vt10x.DefaultBG {
			return x
		}
	}
	return -1
}

// fgParam and bgParam render a vt10x colour as SGR parameters.
//
// vt10x stores palette entries as their index and 24-bit colours as a packed
// r<<16|g<<8|b, which makes a truecolour value below 256 indistinguishable from
// a palette index; such a colour (a near-black blue) renders as the palette
// entry instead. That ambiguity is in the emulator's own encoding, so it cannot
// be resolved here.
func fgParam(c vt10x.Color) string { return colorParam(c, vt10x.DefaultFG, "38", "39") }
func bgParam(c vt10x.Color) string { return colorParam(c, vt10x.DefaultBG, "48", "49") }

func colorParam(c, def vt10x.Color, set, reset string) string {
	switch {
	case c == def || c >= vt10x.DefaultFG:
		return reset
	case c < 256:
		return set + ";5;" + strconv.Itoa(int(c))
	default:
		r, g, bl := (c>>16)&0xFF, (c>>8)&0xFF, c&0xFF
		return set + ";2;" + strconv.Itoa(int(r)) + ";" + strconv.Itoa(int(g)) + ";" + strconv.Itoa(int(bl))
	}
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
