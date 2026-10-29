package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/charmbracelet/x/ansi"
)

// a1Focus is which sessions-tab control receives keys.
type a1Focus int

const (
	focusList a1Focus = iota
	focusPrompt
	focusTodos
)

// applyTrackedSessionMsg handles a to-do load, a failed to-do write, a reply,
// a session usage report, a host rollup, or a PR watch answer. They stay on the global path so a
// finished call still lands after the user leaves the session list.
func (m *Model) applyTrackedSessionMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case todoLoadedMsg:
		return m, m.applyTodoLoaded(msg)
	case todoMutatedMsg:
		return m, m.applyTodoMutated(msg)
	case promptSentMsg:
		return m, m.applyPromptSent(msg)
	case usageMsg:
		return m, m.applyUsageMsg(msg)
	case usageRollupMsg:
		m.applyUsageRollup(msg)
		return m, nil
	case ciStatusMsg:
		return m, m.applyCIStatus(msg)
	case verifyStartedMsg:
		return m, m.applyVerifyStarted(msg)
	case memoryJobsMsg, memoryProposalMsg, memorySavedMsg, memoryJobDoneMsg:
		model, cmd := m.memoryCandidates.Update(msg)
		m.memoryCandidates = model
		m.syncMemoryMenuDesc()
		return m, cmd
	default:
		return m, nil
	}
}

// promptSentMsg is the result of steer.send for one reply.
type promptSentMsg struct {
	host      string
	sessionID string
	sentAt    time.Time
	steer     server.SteerStatus
	err       error
}

// sessionKeysHelp is the long key list `?` shows in place of the hint line.
const sessionKeysHelp = "n: new • e: rename • g: group • enter: collapse • f: filter • c: scope • t: tunnels • s: resume • S: switch agent • y: copy • r: refresh • i: AI • ctrl+k: term • m: menu • q: quit"

const daemonKeysHelp = "agent API: i install • s restart • c reload • u update • r probe • ctrl+k stop • tab: sessions • q: quit"

func newReplyInput() textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 4000
	st := ti.Styles()
	dim := lipgloss.NewStyle().Foreground(a1Dim)
	st.Focused.Placeholder = dim
	st.Blurred.Placeholder = dim
	ti.SetStyles(st)
	applyCursorStyle(&ti)
	return ti
}

// ensurePromptInput builds the reply field once. EchoCharacter is set by
// textinput.New and stays non-zero, which marks the field as initialized.
func (m *Model) ensurePromptInput() {
	if m.promptInput.EchoCharacter != 0 {
		return
	}
	m.promptInput = newReplyInput()
}

func (m *Model) enterPromptFocus() tea.Cmd {
	m.ensurePromptInput()
	if it, ok := m.selectedSessionItem(); ok && !it.header {
		if m.promptFor != it.session.ID {
			m.swapPromptDraft(m.promptFor, it.session.ID)
		}
		m.promptInput.Placeholder = "Reply to " + promptTarget(it.session) + "…"
	}
	m.focus = focusPrompt
	return m.promptInput.Focus()
}

// swapPromptDraft keeps each session's unsent reply with that session.
func (m *Model) swapPromptDraft(from, to string) {
	if from == to {
		return
	}
	m.stashPromptBuf(from)
	m.promptFor = to
	m.ensurePromptInput()
	next := ""
	if m.promptBuf != nil {
		next = m.promptBuf[to]
	}
	m.promptInput.SetValue(next)
}

func (m *Model) stashPromptBuf(id string) {
	if id == "" {
		return
	}
	if m.promptBuf == nil {
		m.promptBuf = map[string]string{}
	}
	text := ""
	if m.promptInput.EchoCharacter != 0 {
		text = m.promptInput.Value()
	}
	if text == "" {
		delete(m.promptBuf, id)
		return
	}
	m.promptBuf[id] = text
}

// forwardWhileReplying keeps the reply line's cursor blinks, and mouse
// over the preview, but does not let the session list move.
func (m *Model) forwardWhileReplying(msg tea.Msg, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	mouse, isMouse := msg.(tea.MouseMsg)
	if !isMouse || mouse.Mouse().X < (m.width/3+4) {
		return m, tea.Batch(cmds...)
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

// handleFocusedKey takes keys while the reply line or the to-do list has focus.
// ctrl+c is not consumed: it still quits. A to-do focus whose session row is
// gone drops the key instead of editing the list.
func (m *Model) handleFocusedKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if msg.String() == "ctrl+c" || (m.focus != focusPrompt && m.focus != focusTodos) {
		return m, nil, false
	}
	m.exitFocusIfSessionGone()
	switch m.focus {
	case focusPrompt:
		model, cmd := m.handlePromptKey(msg)
		return model, cmd, true
	case focusTodos:
		model, cmd := m.handleTodoKey(msg)
		return model, cmd, true
	default:
		return m, nil, true
	}
}

func (m *Model) handlePromptKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.promptInput.Blur()
		m.focus = focusList
		return m, nil
	case "enter":
		if strings.TrimSpace(m.promptInput.Value()) == "" {
			return m, nil
		}
		return m, m.submitPrompt()
	default:
		m.ensurePromptInput()
		var cmd tea.Cmd
		m.promptInput, cmd = m.promptInput.Update(msg)
		return m, cmd
	}
}

func (m *Model) submitPrompt() tea.Cmd {
	m.ensurePromptInput()
	text := strings.TrimSpace(m.promptInput.Value())
	if text == "" {
		return nil
	}
	sess, ok := m.sessionByID(m.promptFor)
	if !ok {
		it, sel := m.selectedSessionItem()
		if !sel || it.header {
			return nil
		}
		sess = it.session
		m.promptFor = sess.ID
	}
	id := sess.ID
	if _, sending := m.promptDraft[id]; sending {
		return nil
	}
	host := sess.RemoteHost
	sentAt := time.Now()
	if m.promptSent == nil {
		m.promptSent = map[string]time.Time{}
	}
	m.promptSent[id] = sentAt
	if m.promptDraft == nil {
		m.promptDraft = map[string]string{}
	}
	m.promptDraft[id] = text
	m.promptInput.SetValue("")

	remote, hasRemote := resolveRemote(m.cfg, sess)
	client := m.todoClient
	return func() tea.Msg {
		if !hasRemote {
			return promptSentMsg{host: host, sessionID: id, sentAt: sentAt, err: fmt.Errorf("no remote for %s", host)}
		}
		if client == nil {
			return promptSentMsg{host: host, sessionID: id, sentAt: sentAt, err: errors.New("todo client is not configured")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		st, err := client.Prompt(ctx, remote, id, text)
		return promptSentMsg{host: host, sessionID: id, sentAt: sentAt, steer: st, err: err}
	}
}

// applyPromptSent records the queue status from steer.send.
// A failed send restores the typed text and clears the send time so the
// line does not claim the note was queued.
func (m *Model) applyPromptSent(msg promptSentMsg) tea.Cmd {
	if m.promptSent != nil {
		if latest, ok := m.promptSent[msg.sessionID]; ok && !latest.Equal(msg.sentAt) {
			return nil
		}
	}
	text := ""
	if m.promptDraft != nil {
		text = m.promptDraft[msg.sessionID]
		delete(m.promptDraft, msg.sessionID)
	}
	if msg.err != nil {
		m.log("reply to %s on %s failed: %v", msg.sessionID, msg.host, msg.err)
		if m.promptSent != nil {
			delete(m.promptSent, msg.sessionID)
		}
		// The note belongs to the session that sent it. The shared field
		// gets it only while that session is the one on screen, and only
		// when nothing newer is already typed there.
		if text != "" {
			m.restoreFailedPrompt(msg.sessionID, text)
		}
		return m.showToast("⚠️  Reply not sent: "+msg.err.Error(), true, 3*time.Second)
	}
	if !msg.sentAt.IsZero() {
		if m.promptSent == nil {
			m.promptSent = map[string]time.Time{}
		}
		m.promptSent[msg.sessionID] = msg.sentAt
	}
	if m.todos == nil {
		m.todos = map[string]todoSnapshot{}
	}
	snap := m.todos[msg.sessionID]
	snap.Steer = mergeSteer(snap.Steer, msg.steer)
	m.todos[msg.sessionID] = snap
	return nil
}

func (m *Model) restoreFailedPrompt(id, text string) {
	if m.promptFor == id {
		if m.promptInput.Value() != "" {
			return
		}
		m.ensurePromptInput()
		m.promptInput.SetValue(text)
		return
	}
	if m.promptBuf != nil && m.promptBuf[id] != "" {
		return
	}
	if m.promptBuf == nil {
		m.promptBuf = map[string]string{}
	}
	m.promptBuf[id] = text
}

func promptTarget(s domain.Session) string {
	if s.IssueKey != "" {
		return s.IssueKey
	}
	if s.Name != "" {
		return s.Name
	}
	return "session"
}

func (m *Model) promptStatus(id string) string {
	if id == "" {
		return ""
	}
	if _, sending := m.promptDraft[id]; sending {
		return "sending…"
	}
	var sent time.Time
	if m.promptSent != nil {
		sent = m.promptSent[id]
	}
	var st server.SteerStatus
	if m.todos != nil {
		st = m.todos[id].Steer
	}
	return promptMarker(sent, st)
}

func (m *Model) sessionPromptLine(width int) string {
	ticket := "session"
	id := ""
	if it, ok := m.selectedSessionItem(); ok {
		ticket = promptTarget(it.session)
		id = it.session.ID
	}
	marker := m.promptStatus(id)
	var input string
	if m.focus == focusPrompt {
		m.ensurePromptInput()
		m.promptInput.Placeholder = "Reply to " + ticket + "…"
		m.promptInput.SetWidth(promptInputWidth(width, marker))
		input = strings.TrimRight(m.promptInput.View(), "\n")
	}
	return renderPromptLine(ticket, input, m.focus == focusPrompt, marker, width)
}

func promptInputWidth(width int, marker string) int {
	room := width - 2
	if marker != "" {
		room -= lipgloss.Width(marker) + 1
	}
	if room < 1 {
		return 1
	}
	return room
}

// renderPromptLine draws the reply row. Unfocused, it is a prompt-coloured
// chevron and a dim "Reply to …". Focused, the chevron sits in front of the
// text input. The marker is the send state for the selected session.
func renderPromptLine(ticket, input string, focused bool, marker string, width int) string {
	if width <= 0 {
		return ""
	}
	chevron := a1Text("› ", a1Prompt, false, false)
	mark := ""
	if marker != "" {
		mark = " " + stylePromptMarker(marker)
	}
	room := width - lipgloss.Width(chevron) - lipgloss.Width(mark)
	if room < 1 {
		return ansi.Truncate(chevron+mark, width, "…")
	}
	body := input
	if !focused {
		body = a1Text("Reply to "+ticket+"…", a1Dim, false, false)
	}
	if lipgloss.Width(body) > room {
		body = ansi.Truncate(body, room, "…")
	}
	return chevron + body + mark
}

func stylePromptMarker(marker string) string {
	fg := a1Dim
	switch marker {
	case "delivered":
		fg = a1Done
	case "not delivered":
		fg = a1Wait
	}
	return a1Text(marker, fg, false, false)
}

func (m *Model) hintLine(width int) string {
	text := hintFor(m.focus)
	if m.focus == focusList {
		if it, ok := m.selectedSessionItem(); ok && offerVerify(it.session, m.gitStatus, reviewerRunning(it.session.ID, m.allSessions)) {
			text = "V verify · " + text
		}
	}
	return lipgloss.NewStyle().Foreground(a1Dim).Render(alignRight(text, m.version, width))
}

// offerVerify is the quiet hint for a worker that has commits to review.
func offerVerify(s domain.Session, st domain.GitStatus, reviewing bool) bool {
	return s.BaseBranch != "" && !s.Role.Hidden() && !reviewing && st.AheadOfBase > 0
}

func hintFor(focus a1Focus) string {
	switch focus {
	case focusPrompt:
		return "enter send · esc back"
	case focusTodos:
		return "a add · e edit · space done · s start · d drop · x delete · J/K move · v expand · esc back"
	default:
		return "ctrl+p reply · ctrl+t to-dos · a attach · ? keys"
	}
}

// a1Chrome is the rows around the panes: header, gap, prompt line, hint line.
// A wrapped key list is one row more; sessionChrome reports that.
const a1Chrome = 4

func (m *Model) sessionChrome() int {
	if m.helpWraps() {
		return a1Chrome + 1
	}
	return a1Chrome
}

// helpWraps reports that `?` is showing a second help line on the sessions tab.
func (m *Model) helpWraps() bool {
	if !m.showKeys || m.currentTab != tabSessions {
		return false
	}
	width := m.width - 4
	if width < 1 {
		width = 1
	}
	_, second := splitHelp(sessionKeysHelp, helpRoom(width, m.version))
	return second != ""
}

// previewHeight is the pane under the detail block. A wrapped key list takes
// one of those rows back, including when the pane is already at its floor.
func (m *Model) previewHeight(mainHeight, detailBudget int) int {
	height := max(6, mainHeight-detailBudget)
	if !m.helpWraps() {
		return height
	}
	opened := max(6, mainHeight+1-detailBudget)
	if height == opened {
		return max(1, opened-1)
	}
	return height
}

// mainFooter is the prompt row plus the hint, or the agent API help on the
// daemons tab. The sessions footer is two rows, matching a1Chrome, or three
// when the key list wraps.
func (m *Model) mainFooter(width int) string {
	if m.currentTab == tabDaemons {
		line := alignRight(daemonKeysHelp, m.version, width)
		return lipgloss.NewStyle().Foreground(lipgloss.Color("241")).MarginTop(1).Render(line)
	}
	below := m.hintLine(width)
	if m.showKeys {
		below = m.keysHelp(width)
	}
	return m.sessionPromptLine(width) + "\n" + below
}

func helpRoom(width int, version string) int {
	verW := lipgloss.Width(version)
	if verW > 0 && width-verW-1 > 0 {
		return width - verW - 1
	}
	return width
}

func (m *Model) keysHelp(width int) string {
	style := lipgloss.NewStyle().Foreground(a1Dim)
	room := helpRoom(width, m.version)
	first, second := splitHelp(sessionKeysHelp, room)
	if second == "" {
		return style.Render(alignRight(first, m.version, width))
	}
	if lipgloss.Width(second) > room {
		second = ansi.Truncate(second, max(1, room), "…")
	}
	return style.Render(first) + "\n" + style.Render(alignRight(second, m.version, width))
}

func splitHelp(text string, width int) (string, string) {
	if width <= 0 || text == "" {
		return "", ""
	}
	if lipgloss.Width(text) <= width {
		return text, ""
	}
	r := []rune(text)
	lo, hi := 1, len(r)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if lipgloss.Width(string(r[:mid])) <= width {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	cut := lo
	line := string(r[:cut])
	if i := strings.LastIndex(line, " "); i > 0 {
		cut = len([]rune(line[:i]))
		line = string(r[:cut])
	}
	rest := strings.TrimSpace(string(r[cut:]))
	if lipgloss.Width(rest) > width {
		rest = ansi.Truncate(rest, width, "…")
	}
	return line, rest
}

func alignRight(left, right string, width int) string {
	if width <= 0 {
		return ""
	}
	if right == "" {
		return ansi.Truncate(left, width, "…")
	}
	if lipgloss.Width(left)+lipgloss.Width(right) <= width {
		pad := width - lipgloss.Width(left) - lipgloss.Width(right)
		return left + strings.Repeat(" ", pad) + right
	}
	room := width - lipgloss.Width(right) - 1
	if room < 1 {
		return ansi.Truncate(right, width, "…")
	}
	return ansi.Truncate(left, room, "…") + " " + right
}
