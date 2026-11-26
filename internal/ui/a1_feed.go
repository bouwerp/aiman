package ui

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bouwerp/aiman/internal/domain"
)

// feedPanelRows is how many actions the collapsed panel lists; expanded it
// lists up to feedPanelExpandedRows.
const (
	feedPanelRows         = 3
	feedPanelExpandedRows = 10
)

var feedRed = lipgloss.Color("1")

// feedRef names one action across hosts.
type feedRef struct {
	host string
	id   int64
}

// feedDismissMenu is the D menu: a reason, the later choice and a note.
type feedDismissMenu struct {
	cursor int
	later  int
	note   textinput.Model
}

// feedEditState is the e editor: which option and its text.
type feedEditState struct {
	option int
	input  textinput.Model
}

type feedReason struct {
	reason domain.FeedDismissReason
	label  string
}

// feedReasons are the D menu's reasons for an action; mute channel only when
// it answers a channel digest.
func (m *Model) feedReasons(it feedItem) []feedReason {
	out := []feedReason{
		{domain.FeedNotRelevant, "not relevant"},
		{domain.FeedHandled, "already handled"},
		{domain.FeedLater, "later"},
		{domain.FeedMuteThread, "mute thread"},
		{domain.FeedMuteSender, "mute sender"},
	}
	if m.feedAnswersDigest(it) {
		out = append(out, feedReason{domain.FeedMuteChannel, "mute channel"})
	}
	return out
}

// feedLaterChoices are the times later can snooze until.
var feedLaterChoices = []string{"1h", "this evening", "tomorrow 09:00"}

func feedLaterUntil(choice int, now time.Time) time.Time {
	y, mo, d := now.Date()
	switch choice {
	case 1:
		evening := time.Date(y, mo, d, 18, 0, 0, 0, now.Location())
		if evening.After(now) {
			return evening
		}
		return now.Add(time.Hour)
	case 2:
		return time.Date(y, mo, d+1, 9, 0, 0, 0, now.Location())
	}
	return now.Add(time.Hour)
}

// selectedFeed is the action under the panel cursor.
func (m *Model) selectedFeed() (feedItem, bool) {
	items := m.feedItems()
	if len(items) == 0 {
		return feedItem{}, false
	}
	if m.feedCursor < 0 || m.feedCursor >= len(items) {
		m.feedCursor = 0
	}
	return items[m.feedCursor], true
}

func (m *Model) selectedFeedKey() feedRef {
	it, ok := m.selectedFeed()
	if !ok {
		return feedRef{}
	}
	return feedRef{host: it.host, id: it.action.ID}
}

// restoreFeedCursor puts the cursor back on k after the list changed, else
// keeps the index within the list.
func (m *Model) restoreFeedCursor(k feedRef) {
	items := m.feedItems()
	for i, it := range items {
		if it.host == k.host && it.action.ID == k.id {
			m.feedCursor = i
			return
		}
	}
	if m.feedCursor >= len(items) {
		m.feedCursor = max(0, len(items)-1)
	}
	if k.id != 0 {
		// The selected action left: its menu or editor goes with it.
		m.feedMenu, m.feedEdit = nil, nil
	}
}

// feedPanelHeight is the rows the panel takes under the session list: none
// while no host has a feed, one dim line with nothing open, else a title,
// the rows and a footer.
func (m *Model) feedPanelHeight() int {
	if len(m.feedHosts()) == 0 {
		return 0
	}
	n := len(m.feedItems())
	if n == 0 {
		return 1
	}
	return 2 + min(n, m.feedPanelLimit())
}

func (m *Model) feedPanelLimit() int {
	if m.feedExpanded {
		return feedPanelExpandedRows
	}
	return feedPanelRows
}

// feedBadge is the header's "feed N (M high)", red while M > 0; "" with no
// open action.
func (m *Model) feedBadge() string {
	open, high := m.feedCounts()
	if open == 0 {
		return ""
	}
	if high > 0 {
		return lipgloss.NewStyle().Foreground(feedRed).Render(fmt.Sprintf("feed %d (%d high)", open, high))
	}
	return a1Text(fmt.Sprintf("feed %d", open), a1Dim, false, false)
}

// renderFeedPanel draws the panel, width columns wide, or "".
func (m *Model) renderFeedPanel(width int) string {
	if len(m.feedHosts()) == 0 || width < 10 {
		return ""
	}
	dim := lipgloss.NewStyle().Foreground(a1Dim)
	items := m.feedItems()
	last := "last batch —"
	if b := m.feedLastBatch(); b != nil {
		last = "last batch " + overviewTime(feedBatchTime(*b))
	}
	if len(items) == 0 {
		return dim.Render(truncatePlain("feed: nothing to do · "+last+m.feedErrNote(), width))
	}
	focused := m.focus == focusFeed
	right := " ctrl+f · F full page ─"
	if focused {
		right = " focused ─"
	}
	title := "─ feed "
	title += strings.Repeat("─", max(1, width-lipgloss.Width(title)-lipgloss.Width(right))) + right
	lines := []string{dim.Render(truncatePlain(title, width))}
	limit := min(len(items), m.feedPanelLimit())
	multi := len(m.feedHosts()) > 1
	for i := 0; i < limit; i++ {
		lines = append(lines, m.feedRow(items[i], focused && i == m.feedCursor, multi, width))
	}
	foot := last
	if more := len(items) - limit; more > 0 {
		foot = fmt.Sprintf("+%d more · %s", more, last)
	}
	foot += m.feedErrNote()
	lines = append(lines, dim.Render(truncatePlain(" "+foot, width)))
	return strings.Join(lines, "\n")
}

func (m *Model) feedErrNote() string {
	for _, host := range m.feedHosts() {
		if e := m.feed[host].err; e != "" {
			return " · " + remoteNameForHost(m.cfg, host) + ": " + firstLineOf(e)
		}
	}
	return ""
}

func firstLineOf(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

// feedRow is one action: cursor, priority, source, short text, and
// "N answers" or "read" on the right.
func (m *Model) feedRow(it feedItem, selected, multi bool, width int) string {
	a := it.action
	cursor := " "
	if selected {
		cursor = "▸"
	}
	glyph := a1Text("·", a1Dim, false, false)
	if a.Priority == "high" {
		glyph = lipgloss.NewStyle().Foreground(feedRed).Render("!")
	}
	source := m.feedSource(it)
	if multi {
		source = remoteNameForHost(m.cfg, it.host) + " " + source
	}
	tag := feedTag(a)
	left := cursor + glyph + " " + fmt.Sprintf("%-7s ", source)
	room := width - lipgloss.Width(left) - lipgloss.Width(tag) - 2
	text := truncatePlain(m.feedShortText(it), max(room, 1))
	gap := width - lipgloss.Width(left) - lipgloss.Width(text) - lipgloss.Width(tag)
	return left + text + strings.Repeat(" ", max(gap, 1)) + a1Text(tag, a1Dim, false, false)
}

// feedTag is "read" for a read action, else the number of answers.
func feedTag(a domain.FeedAction) string {
	if a.State != "proposed" {
		return string(a.State)
	}
	if a.Kind == domain.FeedRead {
		return "read"
	}
	if len(a.Options) == 1 {
		return "1 answer"
	}
	return fmt.Sprintf("%d answers", len(a.Options))
}

// feedSource is the answered event's source, else what the kind implies.
func (m *Model) feedSource(it feedItem) string {
	if ev, ok := m.feedAnswered(it); ok && ev.Source != "" {
		return string(ev.Source)
	}
	switch it.action.Kind {
	case domain.FeedSlackDraft, domain.FeedSlackReply:
		return "slack"
	case domain.FeedReviewPR:
		return "github"
	}
	return "aiman"
}

// feedShortText is the answered event's title, else the rationale's first
// line.
func (m *Model) feedShortText(it feedItem) string {
	if ev, ok := m.feedAnswered(it); ok {
		if t := firstLineOf(ev.Title); t != "" {
			return t
		}
	}
	return firstLineOf(it.action.Rationale)
}

// enterFeedFocus gives the panel the keys and rereads the feed.
func (m *Model) enterFeedFocus() tea.Cmd {
	if len(m.feedHosts()) == 0 {
		return m.showToast("feed: no host has the feed on (Admin Menu → Feed)", false, 4*time.Second)
	}
	m.focus = focusFeed
	m.feedCursor = 0
	m.feedTrace = false
	m.feedMenu, m.feedEdit = nil, nil
	return m.feedFocusFetch()
}

func (m *Model) leaveFeedFocus() {
	m.focus = focusList
	m.feedMenu, m.feedEdit = nil, nil
	m.feedTrace = false
}

// handleFeedKey takes keys while the panel has focus.
func (m *Model) handleFeedKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.feedEdit != nil {
		return m.handleFeedEditKey(msg)
	}
	if m.feedMenu != nil {
		return m.handleFeedMenuKey(msg)
	}
	key := msg.String()
	if key == "esc" {
		m.leaveFeedFocus()
		return m, nil
	}
	if key == "v" {
		m.feedExpanded = !m.feedExpanded
		if m.width > 0 && m.height > 0 {
			m.applySessionChrome()
		}
		return m, nil
	}
	it, ok := m.selectedFeed()
	if !ok {
		return m, nil
	}
	switch key {
	case "j", "down":
		if m.feedCursor < len(m.feedItems())-1 {
			m.feedCursor++
			m.feedTrace = false
		}
	case "k", "up":
		if m.feedCursor > 0 {
			m.feedCursor--
			m.feedTrace = false
		}
	case "1", "2", "3", "4":
		n := int(key[0] - '1')
		if n >= len(it.action.Options) {
			return m, nil
		}
		return m, m.feedApprove(it, n, nil)
	case "enter":
		if len(it.action.Options) == 0 {
			return m, nil
		}
		return m, m.feedApprove(it, 0, nil)
	case "e":
		if len(it.action.Options) == 0 {
			return m, nil
		}
		m.beginFeedEdit(it, 0)
		return m, textinput.Blink
	case "d":
		return m, m.feedActCmd(it.host, map[string]any{"id": it.action.ID, "op": "dismiss", "reason": string(domain.FeedNotRelevant)})
	case "D":
		note := newTodoInput()
		note.Placeholder = "optional note"
		note.Focus()
		m.feedMenu = &feedDismissMenu{note: note}
	case "t":
		m.feedTrace = !m.feedTrace
	case "o":
		return m, m.feedOpen(it)
	}
	return m, nil
}

func (m *Model) feedApprove(it feedItem, option int, params []byte) tea.Cmd {
	p := map[string]any{"id": it.action.ID, "op": "approve", "option": option}
	if params != nil {
		p["params"] = jsonRaw(params)
	}
	return m.feedActCmd(it.host, p)
}

// jsonRaw keeps edited params as JSON on the wire.
type jsonRaw []byte

func (j jsonRaw) MarshalJSON() ([]byte, error) { return j, nil }

func (m *Model) beginFeedEdit(it feedItem, option int) {
	in := newTodoInput()
	in.SetValue(feedOptionText(it.action.Options[option]))
	in.CursorEnd()
	in.Focus()
	m.feedEdit = &feedEditState{option: option, input: in}
}

func (m *Model) handleFeedEditKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	it, ok := m.selectedFeed()
	if !ok {
		m.feedEdit = nil
		return m, nil
	}
	e := m.feedEdit
	switch msg.String() {
	case "esc":
		m.feedEdit = nil
		return m, nil
	case "tab":
		m.beginFeedEdit(it, (e.option+1)%len(it.action.Options))
		return m, nil
	case "enter":
		text := strings.TrimSpace(e.input.Value())
		if text == "" {
			return m, nil
		}
		opt := it.action.Options[e.option]
		m.feedEdit = nil
		if text == strings.TrimSpace(feedOptionText(opt)) {
			return m, m.feedApprove(it, e.option, nil)
		}
		return m, m.feedApprove(it, e.option, feedEditedParams(opt, text))
	}
	var cmd tea.Cmd
	e.input, cmd = e.input.Update(msg)
	return m, cmd
}

func (m *Model) handleFeedMenuKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	it, ok := m.selectedFeed()
	if !ok {
		m.feedMenu = nil
		return m, nil
	}
	menu := m.feedMenu
	reasons := m.feedReasons(it)
	switch msg.String() {
	case "esc":
		m.feedMenu = nil
		return m, nil
	case "up":
		if menu.cursor > 0 {
			menu.cursor--
		}
		return m, nil
	case "down":
		if menu.cursor < len(reasons)-1 {
			menu.cursor++
		}
		return m, nil
	case "left":
		menu.later = (menu.later + len(feedLaterChoices) - 1) % len(feedLaterChoices)
		return m, nil
	case "right":
		menu.later = (menu.later + 1) % len(feedLaterChoices)
		return m, nil
	case "enter":
		r := reasons[min(menu.cursor, len(reasons)-1)]
		p := map[string]any{"id": it.action.ID, "op": "dismiss", "reason": string(r.reason)}
		if note := strings.TrimSpace(menu.note.Value()); note != "" {
			p["note"] = note
		}
		if r.reason == domain.FeedLater {
			p["until"] = feedLaterUntil(menu.later, time.Now()).Format(time.RFC3339)
		}
		m.feedMenu = nil
		return m, m.feedActCmd(it.host, p)
	}
	var cmd tea.Cmd
	menu.note, cmd = menu.note.Update(msg)
	return m, cmd
}

// feedOpen opens the action's link: an aiman:// link selects that session,
// anything else goes to the browser.
func (m *Model) feedOpen(it feedItem) tea.Cmd {
	u := m.feedURL(it)
	if u == "" {
		return m.showToast("feed: this action has no link", false, 3*time.Second)
	}
	if name, ok := strings.CutPrefix(u, "aiman://"); ok {
		if m.selectSessionNamed(name) {
			m.leaveFeedFocus()
			return nil
		}
		return m.showToast("feed: no session "+name, true, 4*time.Second)
	}
	if err := feedOpenURL(u); err != nil {
		return m.showToast("feed: open "+u+": "+err.Error(), true, 5*time.Second)
	}
	return m.showToast("feed: opened "+u, false, 3*time.Second)
}

// feedOpenURL starts the platform's opener. Tests replace it.
var feedOpenURL = func(u string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	return exec.Command(name, u).Start()
}

// selectSessionNamed moves the list cursor to the session with that name.
func (m *Model) selectSessionNamed(name string) bool {
	for i, li := range m.list.Items() {
		if it, ok := li.(item); ok && !it.header && (it.session.Name == name || it.session.ID == name) {
			m.list.Select(i)
			return true
		}
	}
	return false
}

// feedHint is the key line while the panel has focus.
func (m *Model) feedHint() string {
	switch {
	case m.feedEdit != nil:
		return "enter approve edited · tab next option · esc cancel"
	case m.feedMenu != nil:
		return "↑/↓ reason · ←/→ later time · type a note · enter dismiss · esc back"
	}
	return "1-4 approve · e edit · d dismiss · D dismiss… · t trace · o open · v expand · esc back"
}

// renderFeedDetail is the detail pane while the panel has focus: the
// selected action's header, rationale, answers (or the dismiss menu) and
// trace, clipped to height rows.
func (m *Model) renderFeedDetail(width, height int) string {
	it, ok := m.selectedFeed()
	if !ok {
		return "\n  Nothing open in the feed."
	}
	dim := lipgloss.NewStyle().Foreground(a1Dim)
	head := lipgloss.NewStyle().Bold(true)
	a := it.action
	wrap := func(s string, indent int) []string {
		w := max(width-indent, 10)
		var out []string
		for _, l := range strings.Split(ansi.Wordwrap(strings.TrimSpace(s), w, " "), "\n") {
			out = append(out, strings.Repeat(" ", indent)+ansi.Hardwrap(l, w, true))
		}
		return out
	}

	meta := []string{string(a.Kind), m.feedSource(it)}
	if ev, ok := m.feedAnswered(it); ok {
		if ev.ActorName != "" {
			meta = append(meta, ev.ActorName)
		}
		meta = append(meta, overviewTime(ev.At))
	}
	if len(m.feedHosts()) > 1 {
		meta = append(meta, remoteNameForHost(m.cfg, it.host))
	}
	prio := "·"
	if a.Priority == "high" {
		prio = lipgloss.NewStyle().Foreground(feedRed).Render("!")
	}
	lines := []string{prio + " " + truncatePlain(strings.Join(meta, " · "), width-2)}
	lines = append(lines, wrap(a.Rationale, 0)...)
	if a.State != "proposed" {
		lines = append(lines, dim.Render("state: "+string(a.State)))
	}
	lines = append(lines, "")

	if m.feedMenu != nil {
		lines = append(lines, m.feedMenuLines(it, width)...)
	} else {
		lines = append(lines, head.Render("Answers"))
		for i, o := range a.Options {
			marker := " "
			if m.feedEdit != nil && m.feedEdit.option == i {
				marker = "▸"
			}
			label := o.Label
			if k := o.EffectiveKind(a.Kind); k != a.Kind {
				label += dim.Render(" (" + string(k) + ")")
			}
			lines = append(lines, fmt.Sprintf("%s%d %s", marker, i+1, label))
			if m.feedEdit != nil && m.feedEdit.option == i {
				m.feedEdit.input.SetWidth(max(width-6, 10))
				lines = append(lines, "    "+m.feedEdit.input.View())
				continue
			}
			if t := feedOptionText(o); t != o.Label {
				lines = append(lines, wrap(t, 4)...)
			}
		}
		lines = append(lines, dim.Render(" d dismiss: not relevant   D other reason…"))
	}
	lines = append(lines, "")

	toggle := "t expand"
	if m.feedTrace {
		toggle = "t collapse"
	}
	lines = append(lines, head.Render("Trace")+strings.Repeat(" ", max(1, width-5-len(toggle)))+dim.Render(toggle))
	for _, t := range a.Trace {
		if !m.feedTrace && t.Role != "answers" && t.Role != "cause" {
			continue
		}
		role := string(t.Role)
		if role == "context" {
			role = string(t.Kind)
		}
		prefix := fmt.Sprintf(" %s %-8s ", feedTraceTime(t.At), role)
		lines = append(lines, prefix+truncatePlain(firstLineOf(t.Excerpt), max(width-lipgloss.Width(prefix), 4)))
	}
	if height > 0 && len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

func feedTraceTime(t time.Time) string {
	t = t.Local()
	now := time.Now()
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return "    " + t.Format("15:04")
	}
	return t.Format("Mon 15:04")
}

func (m *Model) feedMenuLines(it feedItem, width int) []string {
	dim := lipgloss.NewStyle().Foreground(a1Dim)
	menu := m.feedMenu
	lines := []string{lipgloss.NewStyle().Bold(true).Render("Dismiss because")}
	sender := "the sender"
	if ev, ok := m.feedAnswered(it); ok && ev.ActorName != "" {
		sender = ev.ActorName
	}
	days := m.feedMuteDays()
	for i, r := range m.feedReasons(it) {
		marker := "  "
		if i == menu.cursor {
			marker = "▸ "
		}
		var extra string
		switch r.reason {
		case domain.FeedLater:
			var parts []string
			for j, c := range feedLaterChoices {
				if j == menu.later {
					c = "[" + c + "]"
				}
				parts = append(parts, c)
			}
			extra = strings.Join(parts, " · ")
		case domain.FeedMuteThread, domain.FeedMuteChannel:
			extra = fmt.Sprintf("for %d days", days)
		case domain.FeedMuteSender:
			extra = fmt.Sprintf("%s, for %d days", sender, days)
		}
		line := marker + fmt.Sprintf("%-16s", r.label)
		if extra != "" {
			line += dim.Render(extra)
		}
		lines = append(lines, truncatePlain(line, width))
	}
	menu.note.SetWidth(max(width-8, 10))
	lines = append(lines, "Note: "+menu.note.View())
	return lines
}

// feedMuteDays is the laptop config's mute length, which serve was pushed.
func (m *Model) feedMuteDays() int {
	if m.cfg == nil {
		return 7
	}
	return m.cfg.Feed.MuteDaysOrDefault()
}
