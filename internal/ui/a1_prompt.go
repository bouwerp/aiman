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

// applyTrackedSessionMsg handles a to-do load or a reply result.
// Both stay on the global path so a finished call still lands after the
// user leaves the session list.
func (m *Model) applyTrackedSessionMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case todoLoadedMsg:
		m.applyTodoLoaded(msg)
		return m, nil
	case promptSentMsg:
		return m, m.applyPromptSent(msg)
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
	m.focus = focusPrompt
	if it, ok := m.selectedSessionItem(); ok {
		m.promptInput.Placeholder = "Reply to " + promptTarget(it.session) + "…"
	}
	return m.promptInput.Focus()
}

// handleFocusedKey takes keys while the reply line has focus.
// To-do focus has no key handler here, so those keys stay with the list.
// ctrl+c is not consumed: it still quits.
func (m *Model) handleFocusedKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if m.focus != focusPrompt || msg.String() == "ctrl+c" {
		return m, nil, false
	}
	model, cmd := m.handlePromptKey(msg)
	return model, cmd, true
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
	it, ok := m.selectedSessionItem()
	if !ok {
		return nil
	}
	id := it.session.ID
	host := it.session.RemoteHost
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

	remote, hasRemote := resolveRemote(m.cfg, it.session)
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
		if text != "" {
			m.ensurePromptInput()
			m.promptInput.SetValue(text)
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
	snap.Steer = msg.steer
	m.todos[msg.sessionID] = snap
	return nil
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
	return lipgloss.NewStyle().Foreground(a1Dim).Render(alignRight(hintFor(m.focus), m.version, width))
}

func hintFor(focus a1Focus) string {
	switch focus {
	case focusPrompt:
		return "enter send · esc back"
	case focusTodos:
		return "a add · e edit · space done · s start · d drop · x delete · J/K move · esc back"
	default:
		return "ctrl+p reply · ctrl+t to-dos · a attach · ? keys"
	}
}

// mainFooter is the prompt row plus the hint, or the agent API help on the
// daemons tab. The sessions footer is two rows, matching a1Chrome.
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

func (m *Model) keysHelp(width int) string {
	style := lipgloss.NewStyle().Foreground(a1Dim)
	room := width
	verW := lipgloss.Width(m.version)
	if verW > 0 && width-verW-1 > 0 {
		room = width - verW - 1
	}
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
