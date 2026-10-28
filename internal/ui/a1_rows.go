package ui

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/bubbles/v2/list"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// rowGlyph is the session mark. Failure uses the wait color too: a failed
// row counts as "needs you", and the palette has no separate error color.
func rowGlyph(it item) (string, color.Color) {
	switch {
	case it.needsInput || it.escalated:
		return "●", a1Wait
	case it.activity == "create-failed" || it.activity == "exited" || it.activity == "stale":
		return "✗", a1Wait
	case it.session.AgentEnded || rowListDone(it):
		return "✓", a1Done
	case it.activity == "busy" || it.activity == "creating" || it.activity == "bgwait" || it.activity == "terminating":
		return "◐", a1Work
	default:
		return "○", a1Dim
	}
}

func rowListDone(it item) bool {
	return it.activity == "idle" && it.hasTodos && it.todoDone == it.todoTotal && it.todoTotal > 0
}

// progressBar fills width cells with ━. done/total of those use fill; the rest are faint.
func progressBar(done, total, width int, fill color.Color) string {
	return paintProgress(done, total, width, fill, false)
}

func paintProgress(done, total, width int, fill color.Color, selected bool) string {
	if width <= 0 {
		return ""
	}
	filled := 0
	if total > 0 && done > 0 {
		filled = done * width / total
		if filled > width {
			filled = width
		}
	}
	return a1Text(strings.Repeat("━", filled), fill, false, selected) +
		a1Text(strings.Repeat("━", width-filled), a1Faint, false, selected)
}

func renderA1Row(it item, selected bool, width int) string {
	if width <= 0 {
		return ""
	}
	glyph, gCol := rowGlyph(it)
	indent := rowIndent(it.treeDepth)
	ticket := fitTicket(it.session.IssueKey)
	nameShown, gap := rowNameFit(rowName(it), width, indent, it)
	suffix := rowSuffix(it, width, selected, gCol)

	line := a1Text(indent, a1Dim, false, selected) +
		a1Text(glyph, gCol, false, selected) +
		a1Pad(1, selected) +
		a1Text(ticket, a1BoldText, selected, selected) +
		a1Pad(2, selected) +
		a1Text(nameShown, a1BoldText, false, selected) +
		a1Pad(gap, selected) +
		suffix
	if lipgloss.Width(line) > width {
		line = ansi.Truncate(line, width, "…")
	}
	if selected {
		if short := width - lipgloss.Width(line); short > 0 {
			line += a1Pad(short, true)
		}
	}
	return line
}

// rowIndent is one space on a group root. Nested rows use two spaces per
// level plus a corner; that is the only tree drawing on an A1 row.
func rowIndent(depth int) string {
	if depth <= 0 {
		return " "
	}
	return strings.Repeat("  ", depth) + "└ "
}

func rowName(it item) string {
	name := it.session.Name
	if name == "" {
		name = it.session.TerminalName
	}
	// An expanded parent already shows its children, so the badge is only
	// useful while they are hidden.
	if it.collapsed {
		name += it.childBadge()
	}
	return name
}

func rowNameFit(name string, width int, indent string, it item) (shown string, gap int) {
	suffixW := rowSuffixWidth(it, width)
	prefixW := lipgloss.Width(indent) + 1 + 1 + 9 + 2
	remain := width - prefixW - suffixW
	if remain <= 0 {
		return "", 0
	}
	shown = truncatePlain(name, remain)
	return shown, remain - lipgloss.Width(shown)
}

func rowSuffix(it item, width int, selected bool, fill color.Color) string {
	if !it.hasTodos {
		return ""
	}
	nm := a1Text(fmt.Sprintf("%d/%d", it.todoDone, it.todoTotal), a1Dim, false, selected)
	if width < 30 {
		return nm
	}
	barW := clamp(width/5, 6, 14)
	bar := progressBar(it.todoDone, it.todoTotal, barW, fill)
	if selected {
		bar = paintProgress(it.todoDone, it.todoTotal, barW, fill, true)
	}
	return bar + a1Pad(2, selected) + nm
}

func rowSuffixWidth(it item, width int) int {
	if !it.hasTodos {
		return 0
	}
	nmW := lipgloss.Width(fmt.Sprintf("%d/%d", it.todoDone, it.todoTotal))
	if width < 30 {
		return nmW
	}
	return clamp(width/5, 6, 14) + 2 + nmW
}

func needsYouCount(items []list.Item) int {
	n := 0
	for _, li := range items {
		it, ok := li.(item)
		if !ok || it.header {
			continue
		}
		glyph, _ := rowGlyph(it)
		if glyph == "●" || glyph == "✗" {
			n++
		}
	}
	return n
}

func renderA1Header(host string, sessions, needsYou int, scanning bool, width int) string {
	if width <= 0 {
		return ""
	}
	left := a1Text("aiman", a1BoldText, true, false) + a1Text(" · "+host, a1Dim, false, false)
	right := a1HeaderRight(sessions, needsYou, scanning)
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	line := left + right
	if gap >= 1 {
		line = left + strings.Repeat(" ", gap) + right
	}
	if lipgloss.Width(line) > width {
		return ansi.Truncate(line, width, "…")
	}
	return line
}

func a1HeaderRight(sessions, needsYou int, scanning bool) string {
	if scanning {
		return a1Text("scanning…", a1Dim, false, false)
	}
	prefix := fmt.Sprintf("%d sessions · ", sessions)
	needs := fmt.Sprintf("%d needs you", needsYou)
	if needsYou > 0 {
		return a1Text(prefix, a1Dim, false, false) + a1Text(needs, a1Wait, false, false)
	}
	return a1Text(prefix+needs, a1Dim, false, false)
}

func (m *Model) a1HostLabel() string {
	if m.cfg == nil {
		return "all remotes"
	}
	if m.remoteFilter != "" {
		return remoteNameForHost(m.cfg, m.remoteFilter)
	}
	if len(m.cfg.Remotes) == 1 {
		return remoteNameForHost(m.cfg, m.cfg.Remotes[0].Host)
	}
	return "all remotes"
}

// todoProgressFields is the progress a row shows for one summary.
// A non-zero total, or any rev, means the session has a list. Both zero
// means there is nothing to draw, so the row omits the bar.
func todoProgressFields(sum todoSummary) (done, total int, has bool) {
	return sum.Done, sum.Total, sum.Total > 0 || sum.Rev > 0
}

func fitTicket(key string) string {
	const n = 9
	if lipgloss.Width(key) > n {
		return ansi.Truncate(key, n, "…")
	}
	return key + strings.Repeat(" ", n-lipgloss.Width(key))
}

func truncatePlain(s string, width int) string {
	if s == "" || width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func a1Text(s string, fg color.Color, bold, selected bool) string {
	if s == "" {
		return ""
	}
	st := lipgloss.NewStyle().Foreground(fg)
	if bold {
		st = st.Bold(true)
	}
	if selected {
		st = st.Background(a1SelectedBg)
	}
	return st.Render(s)
}

func a1Pad(n int, selected bool) string {
	if n <= 0 {
		return ""
	}
	spaces := strings.Repeat(" ", n)
	if !selected {
		return spaces
	}
	return a1Text(spaces, a1Dim, false, true)
}
