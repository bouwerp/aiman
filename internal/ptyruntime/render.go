package ptyruntime

import (
	"strconv"
	"strings"
	"unicode"

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

	term, back := newTerminal(cols, rows)
	// Write takes the terminal's own lock, so it must not be called with the
	// lock already held — doing so deadlocks on a non-reentrant mutex. Errors
	// are immaterial: the emulator applies whatever it understands, and a
	// malformed tail should still render everything before it.
	_, _ = term.Write(spool)
	return renderTerminal(term, cols, rows, back.lines)
}

// maxScrollbackLines bounds history kept above the live screen. The preview
// pages through it the way a tmux pane pages through capture-pane -S -.
const maxScrollbackLines = 2000

// scrollBuf is the lines that have left the top of the primary screen.
type scrollBuf struct {
	lines []string
}

func (b *scrollBuf) add(line string) {
	if line == "" || b == nil {
		return
	}
	b.lines = append(b.lines, line)
	if extra := len(b.lines) - maxScrollbackLines; extra > 0 {
		b.lines = append([]string(nil), b.lines[extra:]...)
	}
}

// newTerminal returns an emulator sized for a session, plus the scrollback it
// fills as lines leave the top of the primary screen.
func newTerminal(cols, rows int) (vt10x.Terminal, *scrollBuf) {
	term := vt10x.New()
	term.Resize(cols, rows)
	back := &scrollBuf{}
	vt10x.SetScrollOff(term, func(cells []vt10x.Glyph) {
		back.add(renderGlyphs(cells))
	})
	return term, back
}

// renderTerminal turns an emulator's current screen into text with colour.
//
// Separate from RenderScreen so a long-lived emulator can be rendered without
// replaying its whole history: the runtime keeps one per session and feeds it
// only the bytes that arrived since the last capture.
func renderTerminal(term vt10x.Terminal, cols, rows int, scrollback []string) string {
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
	// Blank rows below the cursor are padding too. A background on those
	// rows is still padding: the live UI is the last row that has a glyph,
	// and following the coloured empty ones hides it. Rows above that are
	// left where the agent put them. Attach paints this grid and the agent
	// addresses the cursor by those row numbers, so collapsing the gap
	// above the composer would leave the caret outside the box. The preview
	// tightens that gap when it displays the capture.
	screen := TrimTrailingBlankRows(strings.TrimRight(b.String(), "\n"))
	if len(scrollback) == 0 {
		return screen
	}
	var out strings.Builder
	for _, line := range scrollback {
		out.WriteString(line)
		out.WriteByte('\n')
	}
	out.WriteString(screen)
	return out.String()
}

// renderGlyphs is one row that has just left the screen, in the same form as
// renderRow so history and the live screen use one colour encoding.
func renderGlyphs(cells []vt10x.Glyph) string {
	last := lastSignificantGlyph(cells)
	if last < 0 {
		return ""
	}
	var b strings.Builder
	writeCells(&b, func(x int) vt10x.Glyph { return cells[x] }, last)
	return b.String()
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
	writeCells(&b, func(x int) vt10x.Glyph { return term.Cell(x, y) }, last)
	return b.String()
}

// writeCells emits columns 0..last with SGR colour changes, and seals the row
// when a colour is still active.
//
// An unterminated run leaks the colour into whatever is drawn next: the rest
// of the row, the following line, or the surrounding UI when the screen is
// embedded in a panel.
func writeCells(b *strings.Builder, cellAt func(int) vt10x.Glyph, last int) {
	fg, bg := vt10x.DefaultFG, vt10x.DefaultBG
	for x := 0; x <= last; x++ {
		cell := cellAt(x)
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
	if fg != vt10x.DefaultFG || bg != vt10x.DefaultBG {
		b.WriteString("\x1b[0m")
	}
}

func lastSignificantGlyph(cells []vt10x.Glyph) int {
	for x := len(cells) - 1; x >= 0; x-- {
		cell := cells[x]
		if cell.Char != 0 && cell.Char != ' ' {
			return x
		}
		if cell.BG != vt10x.DefaultBG {
			return x
		}
	}
	return -1
}

// TrimTrailingBlankRows drops rows at the end of a rendered screen that
// contain no glyphs. A background on those rows is padding, so the preview
// follows empty space instead of the live UI above it. A screen that is
// only a coloured bar is kept, because that bar is the content.
func TrimTrailingBlankRows(screen string) string {
	if screen == "" {
		return ""
	}
	lines := strings.Split(screen, "\n")
	end := len(lines)
	for end > 0 && !rowHasGlyph(lines[end-1]) {
		end--
	}
	if end == 0 || end == len(lines) {
		return screen
	}
	return strings.Join(lines[:end], "\n")
}

// bottomTranscriptRunes is the shortest run of letters and digits that still
// counts as a transcript line. The composer under it is allowed to be long
// too: a status line of that length still sits at the bottom of the grid.
const bottomTranscriptRunes = 40

// bottomChromeRows is how far above the last transcript line the composer's
// padding is allowed to reach. A blank line higher than that is a paragraph.
const bottomChromeRows = 16

// TightenBottomGap drops the blank padding a full-screen agent leaves between
// its transcript and the composer. The composer itself stays. A single blank
// line between two transcript lines stays.
func TightenBottomGap(screen string) string {
	if screen == "" {
		return ""
	}
	lines := strings.Split(screen, "\n")
	last := lastTranscriptRow(lines)
	if last < 0 {
		return screen
	}
	from := chromeBandStart(lines, last)
	// The composer is the last box in the chrome band. Its cursor row is only
	// a border and a block, the same shape as the padding above the box.
	// Deleting that row takes the caret out of the box. Padding above the
	// frame and below it can still go.
	end, resume := len(lines), len(lines)
	if top, bottom, ok := composerFrame(lines); ok {
		end, resume = top, bottom+1
	}
	if from > end {
		from = end
	}
	kept := append([]string(nil), lines[:from]...)
	kept = append(kept, dropChromePadding(lines[from:end])...)
	if resume > end {
		kept = append(kept, lines[end:resume]...)
		kept = append(kept, dropChromePadding(lines[resume:])...)
	}
	if len(kept) == len(lines) {
		return screen
	}
	return strings.Join(kept, "\n")
}

func lastTranscriptRow(lines []string) int {
	last := -1
	for i, line := range lines {
		if textRunes(line) >= bottomTranscriptRunes {
			last = i
		}
	}
	return last
}

// chromeBandStart is the first padding row under the previous transcript line.
// The status line can be as long as a transcript line, so the band reaches up
// through the short composer rows above it. One blank line with no composer
// under it is a paragraph and is left where it is.
func chromeBandStart(lines []string, last int) int {
	start := last + 1
	empties := 0
	chrome := false
	floor := max(0, last-bottomChromeRows)
	for i := last - 1; i >= floor; i-- {
		if textRunes(lines[i]) >= bottomTranscriptRunes {
			break
		}
		start = i
		if !rowHasGlyph(lines[i]) || sparseChrome(lines[i]) {
			empties++
			continue
		}
		chrome = true
	}
	if empties < 2 && !chrome {
		return last + 1
	}
	return start
}

// textRunes counts letters and digits on a rendered row. SGR and box drawing
// are chrome, not a line of the conversation.
func textRunes(line string) int {
	n := 0
	for _, r := range plainRow(line) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			n++
		}
	}
	return n
}

// dropChromePadding removes blank and cursor-only rows. Callers pass the
// band above the composer and the band below it, never the box itself.
func dropChromePadding(lines []string) []string {
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if !rowHasGlyph(line) || sparseChrome(line) {
			continue
		}
		kept = append(kept, line)
	}
	return kept
}

// composerFrame is the composer box near the bottom of the screen: two wide
// rules with the cursor between them. A single rule, or two rules far apart,
// is not that box.
func composerFrame(lines []string) (top, bottom int, ok bool) {
	floor := max(0, len(lines)-bottomChromeRows)
	var rules []int
	for i := floor; i < len(lines); i++ {
		if boxRule(lines[i]) {
			rules = append(rules, i)
		}
	}
	if len(rules) < 2 {
		return 0, 0, false
	}
	top, bottom = rules[0], rules[len(rules)-1]
	if bottom-top > 12 {
		top, bottom = rules[len(rules)-2], rules[len(rules)-1]
		if bottom-top > 12 {
			return 0, 0, false
		}
	}
	return top, bottom, true
}

// boxRule reports a row that is a horizontal edge of a box.
func boxRule(line string) bool {
	n := 0
	for _, r := range plainRow(line) {
		if r < 0x2500 || r > 0x257F {
			continue
		}
		n++
		if n >= 8 {
			return true
		}
	}
	return false
}

// sparseChrome reports a composer row that is only a border or a cursor.
// A letter or a digit keeps the row: that is a prompt, not padding. A rule
// painted across the grid has more than a few marks, so it stays too.
func sparseChrome(line string) bool {
	n := 0
	for _, r := range plainRow(line) {
		if r == ' ' || r == '\t' {
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
		n++
		if n > 3 {
			return false
		}
	}
	return n > 0
}

// plainRow returns the runes of a rendered row with SGR sequences removed.
func plainRow(line string) []rune {
	var b strings.Builder
	for i := 0; i < len(line); i++ {
		if line[i] == 0x1b && i+1 < len(line) && line[i+1] == '[' {
			i += 2
			for i < len(line) && line[i] != 'm' && line[i] != 0x1b {
				i++
			}
			continue
		}
		b.WriteByte(line[i])
	}
	return []rune(b.String())
}

// rowHasGlyph reports whether a rendered row has anything other than spaces.
// SGR sequences are styling, not content.
func rowHasGlyph(line string) bool {
	for i := 0; i < len(line); i++ {
		if line[i] == 0x1b && i+1 < len(line) && line[i+1] == '[' {
			i += 2
			for i < len(line) && line[i] != 'm' && line[i] != 0x1b {
				i++
			}
			continue
		}
		if line[i] != ' ' && line[i] != '\t' {
			return true
		}
	}
	return false
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
