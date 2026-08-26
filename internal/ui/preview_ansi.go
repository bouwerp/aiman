package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// sealPreviewLines terminates any colour left open at the end of each line.
//
// Captured pane content is styled but not self-contained. `tmux capture-pane -e`
// reproduces whatever the agent painted and stops there, so a pane whose agent
// sets a foreground and background per line hands back lines that never reset —
// in one real capture, 53 of 54 lines left colour open and none ended in a
// reset. Written straight into a panel, that state leaks past the end of the
// line and colours whatever is drawn next: the rest of the row, the line below,
// and the dashboard's own chrome. The result reads as a preview that smears its
// colours into the surrounding UI.
//
// Sealing each line keeps the agent's colours (they carry meaning — diffs,
// warnings, dim chrome) while confining them to the line that asked for them.
// Content is otherwise untouched: the viewport does its own ANSI-aware cutting,
// so this must not change any line's visible width.
func sealPreviewLines(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if styleOpenAtEnd(line) {
			lines[i] = line + "\x1b[0m"
		}
	}
	return strings.Join(lines, "\n")
}

// setPreviewContent pushes the captured pane into the preview viewport.
//
// m.tmuxOutput stays the raw capture — it is what change detection compares and
// what `Y` yanks — so the display form is derived here rather than stored.
func (m *Model) setPreviewContent() {
	m.previewCols = widestLine(m.tmuxOutput)
	m.viewport.SetContent(sealPreviewLines(m.tmuxOutput))
}

// widestLine is the visible width of the longest line, ignoring styling.
func widestLine(s string) int {
	widest := 0
	for _, line := range strings.Split(s, "\n") {
		if w := ansi.StringWidth(line); w > widest {
			widest = w
		}
	}
	return widest
}

// previewPanHint tells the user the preview is a window onto a wider screen.
//
// Remote sessions are as wide as the terminal that last sized them — 273
// columns for the session that prompted this — while the panel is a fraction of
// that. The viewport cuts each line to fit and can pan sideways, but without
// being told, a preview whose right edge is sliced mid-word just looks broken.
func (m *Model) previewPanHint() string {
	if m.previewCols <= m.viewport.Width() || m.viewport.Width() <= 0 {
		return ""
	}
	return fmt.Sprintf("  %s", statusStyle.Render(
		fmt.Sprintf("←/→ pan (%d of %d cols)", m.viewport.Width(), m.previewCols)))
}

// sgrState is which channels a line currently has set. Each is tracked
// separately because SGR resets them separately: 39 clears the foreground and
// leaves a background in place, and vice versa.
type sgrState struct{ fg, bg, attr bool }

func (s sgrState) anySet() bool { return s.fg || s.bg || s.attr }

// styleOpenAtEnd reports whether a line finishes with SGR styling still active.
//
// Only SGR sequences (CSI … m) change styling. Non-SGR sequences are skipped
// rather than removed, since removing them would alter the line.
func styleOpenAtEnd(line string) bool {
	var st sgrState
	for i := 0; i < len(line); i++ {
		if line[i] != 0x1b || i+1 >= len(line) || line[i+1] != '[' {
			continue
		}
		j := i + 2
		for j < len(line) && (line[j] == ';' || line[j] == ':' || line[j] == '?' ||
			(line[j] >= '0' && line[j] <= '9')) {
			j++
		}
		if j >= len(line) {
			break // truncated sequence at end of line; nothing reliable to read
		}
		if line[j] == 'm' {
			applySGR(line[i+2:j], &st)
		}
		i = j
	}
	return st.anySet()
}

// applySGR folds one SGR parameter list into the running state.
//
// The extended-colour forms have to be consumed as a unit: in "38;5;1" the 5
// and 1 are the palette selector and index, and reading them as standalone
// parameters would see a blink attribute and a bold attribute that were never
// set. Unknown parameters are ignored rather than guessed at.
func applySGR(params string, st *sgrState) {
	if params == "" {
		*st = sgrState{} // bare CSI m is a full reset
		return
	}
	fields := strings.Split(params, ";")
	for i := 0; i < len(fields); i++ {
		n, err := strconv.Atoi(strings.TrimSpace(fields[i]))
		if err != nil {
			continue
		}
		switch {
		case n == 0:
			*st = sgrState{}
		case n == 38 || n == 48:
			// Extended colour: 5;<idx> or 2;<r>;<g>;<b>.
			if n == 38 {
				st.fg = true
			} else {
				st.bg = true
			}
			i += extendedColorParams(fields, i)
		case n == 39:
			st.fg = false
		case n == 49:
			st.bg = false
		case (n >= 30 && n <= 37) || (n >= 90 && n <= 97):
			st.fg = true
		case (n >= 40 && n <= 47) || (n >= 100 && n <= 107):
			st.bg = true
		case n >= 1 && n <= 9:
			st.attr = true
		case n >= 21 && n <= 29:
			// Attribute-off codes (normal intensity, no underline, and so on).
			st.attr = false
		}
	}
}

// extendedColorParams returns how many parameters after fields[i] belong to an
// extended-colour introducer, so the caller can skip them.
func extendedColorParams(fields []string, i int) int {
	if i+1 >= len(fields) {
		return 0
	}
	switch strings.TrimSpace(fields[i+1]) {
	case "5": // 5;<index>
		return min(2, len(fields)-1-i)
	case "2": // 2;<r>;<g>;<b>
		return min(4, len(fields)-1-i)
	default:
		return 0
	}
}
