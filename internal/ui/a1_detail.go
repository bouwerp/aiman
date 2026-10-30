package ui

import (
	"fmt"
	"image/color"
	"math"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/bouwerp/aiman/internal/coordinator"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/review"
	"github.com/charmbracelet/x/ansi"
)

// detailMetaExtras is cost, token, cache, and review state for one session.
// usage.get fills the cost fields. Reviewing and WatchingPR are not part of that report.
type detailMetaExtras struct {
	CostUSD        float64
	HasCost        bool
	Tokens         int64
	CacheRatio     float64
	HasCache       bool
	ContextPct     float64 // latest turn / window; 0 when the window is unset
	ContextWindow  int64   // 0 hides the gauge
	BudgetPct      float64 // used / budget; 0 hides the marker
	BudgetState    string  // "", "warn", "over"
	Reviewing      bool
	VerifyAdvisory bool
	WatchingPR     int
}

// todoView is one session's to-do list as the detail pane draws it.
// Cursor -1 is unfocused. Input is an already rendered add or edit line,
// empty when the row is not being edited.
type todoView struct {
	Snap     todoSnapshot
	Cursor   int
	Question []string
	Input    string
	// RefGlyph replaces a pending row's ○ with the child session's glyph.
	RefGlyph func(ref string) (glyph string, ok bool)
	// Expanded lifts the maxTodoRows cap up to MaxRows, the rows the pane
	// has for the whole list block. MaxRows <= 0 keeps the collapsed cap.
	Expanded bool
	MaxRows  int
}

// todoExpandKey toggles the expanded list while the to-dos are focused.
const todoExpandKey = "v"

// maxTodoRows is the collapsed to-do window. A longer list keeps the
// focused row visible and spends the last row on the hidden count.
const maxTodoRows = 8

func renderMetaLine(s domain.Session, x detailMetaExtras, now time.Time, width int) string {
	var parts []string
	add := func(text string) {
		if text != "" {
			parts = append(parts, dimText(text))
		}
	}
	add(metaAgent(s))
	add(s.Tier)
	if !s.CreatedAt.IsZero() {
		add(shortElapsed(now.Sub(s.CreatedAt)))
	}
	add(s.Branch)
	if ref, err := review.ParseRef(s.ReviewPR); err == nil {
		add(fmt.Sprintf("review #%d", ref.Number))
	}
	if x.HasCost {
		add(fmt.Sprintf("~$%.2f", x.CostUSD))
	} else if x.Tokens > 0 {
		add(formatTokens(x.Tokens))
	}
	if x.HasCache {
		add(fmt.Sprintf("cache %d%%", int(math.Round(x.CacheRatio*100))))
	}
	if gauge := contextGauge(x); gauge != "" {
		parts = append(parts, gauge)
	}
	if marker := budgetMarker(x); marker != "" {
		parts = append(parts, marker)
	}
	if x.Reviewing {
		add("reviewing…")
	}
	if x.VerifyAdvisory {
		add("verify advisory")
	}
	if x.WatchingPR > 0 {
		add(fmt.Sprintf("watching PR #%d", x.WatchingPR))
	}
	if len(parts) == 0 {
		return ""
	}
	return truncateToWidth(joinParts(parts), width)
}

// budgetMarker is a dim budget percentage on the meta line.
// Only warn (70%+) and over (100%+) show; below that stays quiet.
func budgetMarker(x detailMetaExtras) string {
	if x.BudgetState == "" {
		return ""
	}
	return dimText(fmt.Sprintf("budget %d%%", int(math.Round(x.BudgetPct*100))))
}

// contextGauge is "ctx N%" after the cache percentage.
// A zero window appends nothing. At or below 80% the text is dim. Above 80%
// it uses the detail pane's warn color. Above 95% it uses the CI failure red.
func contextGauge(x detailMetaExtras) string {
	if x.ContextWindow <= 0 {
		return ""
	}
	pct := x.ContextPct
	if pct < 0 {
		pct = 0
	}
	text := fmt.Sprintf("ctx %d%%", int(math.Round(pct*100)))
	return a1Text(text, contextGaugeColor(pct), false, false)
}

func contextGaugeColor(pct float64) color.Color {
	switch {
	case pct > 0.95:
		return checksColor("failure")
	case pct > 0.80:
		return a1Wait
	default:
		return a1Dim
	}
}

func metaAgent(s domain.Session) string {
	return strings.TrimSpace(strings.TrimSpace(s.AgentName) + " " + strings.TrimSpace(s.AgentModel))
}

// shortElapsed is the age of a session on the meta line.
// Under a minute is "now"; otherwise the largest unit, "42m", "3h", or "2d".
func shortElapsed(d time.Duration) string {
	if d < time.Minute {
		return "now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}

func formatTokens(n int64) string {
	if n >= 1_000_000 {
		if n%1_000_000 == 0 {
			return fmt.Sprintf("%dM tok", n/1_000_000)
		}
		return fmt.Sprintf("%.1fM tok", float64(n)/1_000_000)
	}
	k := n / 1000
	if k < 1 {
		k = 1
	}
	return fmt.Sprintf("%dk tok", k)
}

// renderGitPRLine is the one-line git and pull-request summary.
// A branch name alone draws nothing: ahead, behind, a dirty count, or a PR
// is what makes the line exist. "No open PR" is only appended on that line.
func renderGitPRLine(st domain.GitStatus, width int) string {
	if width <= 0 {
		return ""
	}
	changed := st.StagedCount + st.UnstagedCount + st.UntrackedCount
	drift := st.Ahead != 0 || st.Behind != 0
	if !drift && changed == 0 && st.PullRequest == nil {
		return ""
	}
	var left []string
	if drift {
		left = append(left, dimText(fmt.Sprintf("↑%d↓%d", st.Ahead, st.Behind)))
	}
	if changed > 0 {
		left = append(left, dimText(fmt.Sprintf("%d changed", changed)))
	}
	right := gitRightParts(st.PullRequest)
	for {
		line := joinParts(append(append([]string{}, left...), right...))
		if lipgloss.Width(line) <= width {
			return line
		}
		if len(left) > 0 {
			left = left[1:]
			continue
		}
		if len(right) > 1 {
			right = right[:len(right)-1]
			continue
		}
		return ansi.Truncate(line, width, "…")
	}
}

func gitRightParts(pr *domain.PullRequest) []string {
	if pr == nil {
		return []string{dimText("No open PR")}
	}
	parts := []string{renderPRIdentity(pr)}
	if ci := renderCI(pr); ci != "" {
		parts = append(parts, ci)
	}
	return parts
}

func renderPRIdentity(pr *domain.PullRequest) string {
	state := pr.DisplayState
	if state == "" {
		state = pr.State
	}
	eff, label := reviewPresentation(pr)
	line := dimText(fmt.Sprintf("PR #%d", pr.Number))
	if state != "" {
		line += dimText(" " + state)
	}
	if label != "" {
		line += dimText(" ") + lipgloss.NewStyle().Foreground(prReviewForeground(eff)).Render(label)
	}
	return line
}

func reviewPresentation(pr *domain.PullRequest) (eff, label string) {
	label = pr.ReviewStatus
	if label == "" && pr.ReviewDecision != "" {
		label = strings.ToLower(pr.ReviewDecision)
	}
	eff = pr.ReviewStatus
	if eff != "" {
		return eff, label
	}
	switch strings.ToUpper(strings.TrimSpace(pr.ReviewDecision)) {
	case "APPROVED":
		eff = "approved"
	case "CHANGES_REQUESTED":
		eff = "changes_requested"
	default:
		eff = label
	}
	return eff, label
}

func renderCI(pr *domain.PullRequest) string {
	if pr.ChecksSummary == "" || pr.ChecksStatus == "none" {
		return ""
	}
	return lipgloss.NewStyle().Foreground(checksColor(pr.ChecksStatus)).Render("CI:" + pr.ChecksSummary)
}

func checksColor(status string) color.Color {
	switch status {
	case "success":
		return lipgloss.Color("#00FF00")
	case "failure":
		return lipgloss.Color("#FF0000")
	case "pending":
		return lipgloss.Color("#FFA500")
	default:
		return lipgloss.Color("#7D7D7D")
	}
}

func renderTodoLines(v todoView, width int) []string {
	if width <= 0 {
		return nil
	}
	if !v.Snap.HasList {
		return statusThenFollow(dimText("no to-do list yet"), v.Question, v.Input, width)
	}
	items := v.Snap.List.Items
	order := todoDisplayOrder(items)
	question := formatQuestionLines(v.Question, width)
	start, end, more := todoWindow(len(order), todoFocus(v, order), v.itemRows(len(question)))
	rows := make([]string, 0, end-start+len(question)+2)
	activeRow := -1
	for pos := start; pos < end; pos++ {
		i := order[pos]
		if activeRow < 0 && items[i].State == domain.TodoActive {
			activeRow = len(rows)
		}
		rows = append(rows, renderTodoRow(items[i], v.Snap.List.NotInAgent, v.Cursor == i, width, v.RefGlyph))
	}
	rows = insertLines(rows, questionAt(activeRow, len(rows)), question)
	if more > 0 {
		rows = append(rows, truncateToWidth(dimText(todoMoreText(items, order, start, end, v)), width))
	}
	if v.Input != "" {
		rows = append(rows, truncateToWidth(v.Input, width))
	}
	return rows
}

// itemRows is how many rows the item window may use. Collapsed is
// maxTodoRows. Expanded uses MaxRows, the rows the pane has for the whole
// list block, less the question and input lines drawn around the items.
func (v todoView) itemRows(questionLines int) int {
	if !v.Expanded || v.MaxRows <= 0 {
		return maxTodoRows
	}
	rows := v.MaxRows - questionLines
	if v.Input != "" {
		rows--
	}
	if rows < 2 {
		rows = 2
	}
	return rows
}

// todoMoreText names what the overflow row hides. A collapsed list also
// says how to expand it; a focused one only names the key.
func todoMoreText(items []domain.TodoItem, order []int, start, end int, v todoView) string {
	hidden := len(order) - (end - start)
	text := fmt.Sprintf("+%d more", hidden)
	done := 0
	for pos, i := range order {
		if (pos < start || pos >= end) && items[i].State == domain.TodoDone {
			done++
		}
	}
	if done > 0 {
		text += fmt.Sprintf(" (%d done)", done)
	}
	switch {
	case v.Expanded:
		return text
	case v.Cursor >= 0:
		return text + " · " + todoExpandKey + " to expand"
	default:
		return text + " · ctrl+t then " + todoExpandKey + " to expand"
	}
}

// todoDisplayOrder lists item indexes in the order the panel draws them:
// active, then every other open state, then done, then dropped. List order
// holds within a group. Stored order is untouched; callers map a display
// position back to the real item through the returned indexes.
func todoDisplayOrder(items []domain.TodoItem) []int {
	rank := func(s domain.TodoState) int {
		switch s {
		case domain.TodoActive:
			return 0
		case domain.TodoDone:
			return 2
		case domain.TodoDropped:
			return 3
		default:
			return 1
		}
	}
	order := make([]int, len(items))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return rank(items[order[a]].State) < rank(items[order[b]].State)
	})
	return order
}

// todoFocus is the display position the window must keep visible.
func todoFocus(v todoView, order []int) int {
	if v.Cursor >= 0 {
		for pos, i := range order {
			if i == v.Cursor {
				return pos
			}
		}
	}
	return 0
}

// todoWindow picks the slice of display positions that contains target.
// A list longer than rows reserves the last row for the hidden count.
func todoWindow(n, target, rows int) (start, end, more int) {
	if n <= 0 {
		return 0, 0, 0
	}
	if n <= rows {
		return 0, n, 0
	}
	visible := rows - 1
	if target < 0 {
		target = 0
	}
	if target >= n {
		target = n - 1
	}
	start = target - visible + 1
	if start < 0 {
		start = 0
	}
	end = start + visible
	if end > n {
		end = n
		start = end - visible
	}
	if start < 0 {
		start = 0
	}
	return start, end, n - (end - start)
}

func renderTodoRow(it domain.TodoItem, notIn []string, selected bool, width int, refGlyph func(string) (string, bool)) string {
	line := todoGlyphAndText(it, selected, refGlyph)
	if todoNotInAgent(it.ID, notIn) {
		line += a1Text(" ⚠ not in agent's list", a1Wait, false, selected)
	}
	line = truncateToWidth(line, width)
	if !selected {
		return line
	}
	if gap := width - lipgloss.Width(line); gap > 0 {
		line += a1Pad(gap, true)
	}
	return line
}

func todoGlyphAndText(it domain.TodoItem, selected bool, refGlyph func(string) (string, bool)) string {
	g := it.State.Glyph()
	if it.State == domain.TodoPending && refGlyph != nil {
		if id, ok := coordinator.ChildID(it.Ref); ok {
			if glyph, ok := refGlyph(id); ok {
				g = glyph
			}
		}
	}
	switch it.State {
	case domain.TodoDone:
		return a1Text(g+" ", a1Dim, false, selected) + a1Text(it.Text, a1Dim, false, selected)
	case domain.TodoActive:
		return a1Text(g+" ", a1Wait, false, selected) + a1Text(it.Text, a1BoldText, true, selected)
	case domain.TodoDropped:
		return a1Text(g+" ", a1Dim, false, selected) + droppedText(it.Text, selected)
	default:
		text := it.Text
		if selected {
			text = a1Text(it.Text, a1BoldText, false, true)
		}
		return a1Text(g+" ", a1Dim, false, selected) + text
	}
}

func droppedText(text string, selected bool) string {
	st := lipgloss.NewStyle().Foreground(a1Dim).Strikethrough(true)
	if selected {
		st = st.Background(a1SelectedBg)
	}
	return st.Render(text)
}

func todoNotInAgent(id string, ids []string) bool {
	for _, other := range ids {
		if other == id {
			return true
		}
	}
	return false
}

func questionAt(activeRow, n int) int {
	if activeRow < 0 {
		return n
	}
	return activeRow + 1
}

func formatQuestionLines(lines []string, width int) []string {
	if len(lines) == 0 {
		return nil
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, truncateToWidth("  "+a1Text("│ "+line, a1Wait, false, false), width))
	}
	return out
}

// waitingQuestion wraps a hook question to at most three lines.
// Any other hook state, including idle with a message, draws nothing.
func waitingQuestion(it item, width int) []string {
	if it.session.HookState != domain.AgentStateWaitingInput || it.session.HookStateMessage == "" {
		return nil
	}
	return wrapPlain(it.session.HookStateMessage, questionTextWidth(width), 3)
}

func questionTextWidth(width int) int {
	w := width - lipgloss.Width("  │ ")
	if w < 1 {
		return 1
	}
	return w
}

func wrapPlain(s string, width, maxLines int) []string {
	words := strings.Fields(s)
	if len(words) == 0 || maxLines <= 0 || width <= 0 {
		return nil
	}
	var lines []string
	cur := ""
	for _, w := range words {
		if len(lines) == maxLines {
			break
		}
		next, wrapped := nextWrap(cur, w, width)
		if wrapped && cur != "" {
			lines = append(lines, cur)
			cur = ""
			if len(lines) == maxLines {
				break
			}
			next, _ = nextWrap("", w, width)
		}
		cur = next
	}
	if cur != "" && len(lines) < maxLines {
		lines = append(lines, cur)
	}
	return lines
}

func nextWrap(cur, word string, width int) (string, bool) {
	if lipgloss.Width(word) > width {
		if cur != "" {
			return cur, true
		}
		return truncatePlain(word, width), false
	}
	if cur == "" {
		return word, false
	}
	next := cur + " " + word
	if lipgloss.Width(next) > width {
		return cur, true
	}
	return next, false
}

func renderDetailTitle(s domain.Session, width int) string {
	var text string
	switch {
	case s.IssueKey != "" && s.Name != "":
		text = s.IssueKey + " · " + s.Name
	case s.IssueKey != "":
		text = s.IssueKey
	default:
		text = s.Name
	}
	if text == "" {
		return ""
	}
	return truncateToWidth(a1Text(text, a1BoldText, true, false), width)
}

// minPreviewRows is the preview height an expanded to-do list leaves alone.
const minPreviewRows = 3

// fitSessionColumn clips the detail so the column stays within `column`
// rows, and returns the preview height that is left. The viewport's own
// border is part of the rendered height, so the height passed to SetHeight
// is whatever still fits, not a fixed floor.
func (m *Model) fitSessionColumn(it item, width int, toast string, column int) (string, int) {
	if column < 1 {
		m.viewport.SetHeight(0)
		return "", 0
	}
	caption := m.renderPreviewCaption(width)
	m.detailRoom = column - lipgloss.Height(composeAbovePreview("", toast, caption)) - minPreviewRows
	detail := m.renderSessionDetail(it, width)
	m.detailRoom = 0
	lines := strings.Split(detail, "\n")
	if detail == "" {
		lines = nil
	}
	for {
		body := ""
		if len(lines) > 0 {
			body = strings.Join(lines, "\n")
		}
		aboveH := lipgloss.Height(composeAbovePreview(body, toast, caption))
		room := column - aboveH
		if preview, ok := m.previewHeightWithin(room); ok {
			return body, preview
		}
		if len(lines) == 0 {
			m.viewport.SetHeight(0)
			return "", 0
		}
		lines = lines[:len(lines)-1]
	}
}

// previewHeightWithin is the largest SetHeight whose rendered viewport is
// at most room rows. The viewport style can draw more rows than SetHeight
// when the height is smaller than its frame.
func (m *Model) previewHeightWithin(room int) (int, bool) {
	for preview := room; preview > 0; preview-- {
		m.viewport.SetHeight(preview)
		if lipgloss.Height(m.viewport.View()) <= room {
			return preview, true
		}
	}
	return 0, false
}

// composeAbovePreview is the detail, toast, and caption without the newline
// that joins the preview. Height of this string plus the preview height is
// the column.
func composeAbovePreview(detail, toast, caption string) string {
	var b strings.Builder
	if detail != "" {
		b.WriteString(detail)
		b.WriteByte('\n')
	}
	if toast != "" {
		b.WriteString(strings.TrimPrefix(toast, "\n"))
	}
	b.WriteString(caption)
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) renderSessionDetail(it item, width int) string {
	var head []string
	if title := renderDetailTitle(it.session, width); title != "" {
		head = append(head, title)
	}
	extras := m.metaFor(it.session.ID)
	extras.Reviewing = reviewerRunning(it.session.ID, m.allSessions)
	extras.VerifyAdvisory = it.session.Unprotected
	if meta := renderMetaLine(it.session, extras, time.Now(), width); meta != "" {
		head = append(head, meta)
	}
	if git := renderGitPRLine(m.gitStatus, width); git != "" {
		head = append(head, git)
	}
	m.todoRowBudget = 0
	if m.detailRoom > 0 {
		m.todoRowBudget = m.detailRoom - len(head) - 1
	}
	todos := m.sessionTodoLines(it, width)
	lines := head
	if len(head) > 0 && len(todos) > 0 {
		lines = append(lines, "")
	}
	lines = append(lines, todos...)
	return strings.Join(lines, "\n")
}

func (m *Model) metaFor(id string) detailMetaExtras {
	if m.metaExtras == nil {
		return detailMetaExtras{}
	}
	return m.metaExtras[id]
}

func (m *Model) sessionTodoLines(it item, width int) []string {
	if m.todoUnsupported[todoRemoteHost(m.cfg, it.session)] {
		return statusThenFollow(dimText("to-dos unavailable on this remote"), waitingQuestion(it, width), "", width)
	}
	snap := todoSnapshot{}
	if m.todos != nil {
		snap = m.todos[it.session.ID]
	}
	cursor := -1
	input := ""
	if m.focus == focusTodos && it.session.ID == m.focusedSessionID {
		cursor = m.todoCursor
		input = m.todoEditLine(width)
	}
	return renderTodoLines(todoView{
		Snap:     snap,
		Cursor:   cursor,
		Question: waitingQuestion(it, width),
		Input:    input,
		RefGlyph: m.todoRefGlyph,
		Expanded: m.todoExpanded[it.session.ID],
		MaxRows:  m.todoRowBudget,
	}, width)
}

// todoRefGlyph maps a linked child session to the glyph its row uses.
func (m *Model) todoRefGlyph(childID string) (string, bool) {
	for _, raw := range m.sessionsForHeader() {
		row, ok := raw.(item)
		if !ok || row.session.ID != childID {
			continue
		}
		glyph, _ := rowGlyph(row)
		return glyph, glyph != ""
	}
	return "", false
}

func dimText(s string) string {
	return a1Text(s, a1Dim, false, false)
}

// statusThenFollow draws a waiting question, and an add/edit line, under a
// status row. The status is not a list, but the question still belongs there.
func statusThenFollow(status string, question []string, input string, width int) []string {
	lines := []string{truncateToWidth(status, width)}
	lines = append(lines, formatQuestionLines(question, width)...)
	if input != "" {
		lines = append(lines, truncateToWidth(input, width))
	}
	return lines
}

func joinParts(parts []string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(ansi.Strip(p)) == "" {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, dimText(" · "))
}

func insertLines(dst []string, at int, extra []string) []string {
	if len(extra) == 0 {
		return dst
	}
	if at < 0 {
		at = 0
	}
	if at > len(dst) {
		at = len(dst)
	}
	out := make([]string, 0, len(dst)+len(extra))
	out = append(out, dst[:at]...)
	out = append(out, extra...)
	out = append(out, dst[at:]...)
	return out
}

func truncateToWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "…")
}
