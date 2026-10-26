package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/domain"
)

// todoEditState is an open add or edit on the focused to-do list.
// An empty id means add. after is the item the new row follows.
type todoEditState struct {
	id    string
	after string
}

// todoMutatedMsg is a failed to-do write.
// A successful write comes back as todoLoadedMsg, after Apply and Get.
type todoMutatedMsg struct {
	host      string
	sessionID string
	err       error
}

func newTodoInput() textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 4000
	applyCursorStyle(&ti)
	return ti
}

// ensureTodoInput builds the add/edit field once. EchoCharacter is set by
// textinput.New and stays non-zero, which marks the field as initialized.
func (m *Model) ensureTodoInput() {
	if m.todoInput.EchoCharacter != 0 {
		return
	}
	m.todoInput = newTodoInput()
}

// enterTodoFocus records the selected session and parks the cursor on its
// active item, or on the first row when nothing is active.
func (m *Model) enterTodoFocus() {
	it, ok := m.selectedSessionItem()
	if !ok {
		return
	}
	m.focusedSessionID = it.session.ID
	m.focus = focusTodos
	m.todoEdit = nil
	m.todoCursor = 0
	for i, item := range m.focusedTodoItems() {
		if item.State == domain.TodoActive {
			m.todoCursor = i
			break
		}
	}
}

// exitFocusIfSessionGone returns to the list when the selected row is not
// the session whose to-dos are focused. A header, an empty selection, or a
// different session all count. The key that noticed this is not applied.
func (m *Model) exitFocusIfSessionGone() {
	if m.focus != focusTodos {
		return
	}
	it, ok := m.selectedSessionItem()
	if ok && it.session.ID == m.focusedSessionID {
		return
	}
	m.leaveTodoFocus()
}

func (m *Model) leaveTodoFocus() {
	m.cancelTodoEdit()
	m.focus = focusList
	m.focusedSessionID = ""
	m.todoCursor = 0
}

func (m *Model) handleTodoKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.todoEdit != nil {
		return m.handleTodoEditKey(msg)
	}
	if len(m.focusedTodoItems()) == 0 {
		return m.handleEmptyTodoKey(msg)
	}
	if cmd, ok := m.todoMotion(msg.String()); ok {
		return m, cmd
	}
	switch msg.String() {
	case "space", " ":
		return m, m.toggleFocusedTodo()
	case "s":
		return m, m.setFocusedTodoState(domain.TodoActive)
	case "d":
		return m, m.setFocusedTodoState(domain.TodoDropped)
	case "x":
		return m, m.removeFocusedTodo()
	case "e":
		return m, m.beginEditFocusedTodo()
	case "a":
		return m, m.beginAddTodo()
	default:
		return m, nil
	}
}

func (m *Model) handleEmptyTodoKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.leaveTodoFocus()
		return m, nil
	case "a":
		return m, m.beginAddTodo()
	default:
		return m, nil
	}
}

func (m *Model) todoMotion(key string) (tea.Cmd, bool) {
	switch key {
	case "esc":
		m.leaveTodoFocus()
		return nil, true
	case "up", "k":
		m.moveTodoCursor(-1)
		return nil, true
	case "down", "j":
		m.moveTodoCursor(1)
		return nil, true
	case "K":
		return m.moveFocusedTodo(-1), true
	case "J":
		return m.moveFocusedTodo(1), true
	default:
		return nil, false
	}
}

func (m *Model) handleTodoEditKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.cancelTodoEdit()
		return m, nil
	case "enter":
		return m, m.commitTodoEdit()
	default:
		m.ensureTodoInput()
		var cmd tea.Cmd
		m.todoInput, cmd = m.todoInput.Update(msg)
		return m, cmd
	}
}

func (m *Model) beginEditFocusedTodo() tea.Cmd {
	it, ok := m.todoItemAtCursor()
	if !ok {
		return nil
	}
	return m.openTodoInput(it.ID, "", it.Text)
}

func (m *Model) beginAddTodo() tea.Cmd {
	after := ""
	if it, ok := m.todoItemAtCursor(); ok {
		after = it.ID
	}
	return m.openTodoInput("", after, "")
}

func (m *Model) openTodoInput(id, after, text string) tea.Cmd {
	m.ensureTodoInput()
	m.todoEdit = &todoEditState{id: id, after: after}
	m.todoInput.SetValue(text)
	m.todoInput.CursorEnd()
	return m.todoInput.Focus()
}

func (m *Model) cancelTodoEdit() {
	m.todoEdit = nil
	if m.todoInput.EchoCharacter == 0 {
		return
	}
	m.todoInput.Blur()
	m.todoInput.SetValue("")
}

func (m *Model) commitTodoEdit() tea.Cmd {
	if m.todoEdit == nil {
		return nil
	}
	edit := *m.todoEdit
	text := strings.TrimSpace(m.todoInput.Value())
	m.cancelTodoEdit()
	if edit.id == "" {
		if text == "" {
			return nil
		}
		return m.mutateFocused(opAdd(m.focusedSessionID, text, edit.after))
	}
	if current, ok := m.todoItemByID(edit.id); ok && text == current.Text {
		return nil
	}
	return m.mutateFocused(opUpdateText(m.focusedSessionID, edit.id, text))
}

func (m *Model) toggleFocusedTodo() tea.Cmd {
	it, ok := m.todoItemAtCursor()
	if !ok {
		return nil
	}
	st := domain.TodoDone
	if it.State == domain.TodoDone {
		st = domain.TodoPending
	}
	return m.setFocusedTodoState(st)
}

func (m *Model) setFocusedTodoState(st domain.TodoState) tea.Cmd {
	it, ok := m.todoItemAtCursor()
	if !ok {
		return nil
	}
	return m.mutateFocused(opSetState(m.focusedSessionID, it.ID, st))
}

func (m *Model) removeFocusedTodo() tea.Cmd {
	it, ok := m.todoItemAtCursor()
	if !ok {
		return nil
	}
	return m.mutateFocused(opRemove(m.focusedSessionID, it.ID))
}

// moveFocusedTodo sends the move. The ends are a no-op.
// The cursor stays on the item when the reloaded list lands.
func (m *Model) moveFocusedTodo(delta int) tea.Cmd {
	items := m.focusedTodoItems()
	from := m.todoCursor
	to := from + delta
	if from < 0 || from >= len(items) || to < 0 || to >= len(items) {
		return nil
	}
	return m.mutateFocused(opMove(m.focusedSessionID, items[from].ID, to))
}

func (m *Model) mutateFocused(op todoOp) tea.Cmd {
	sess, ok := m.sessionByID(m.focusedSessionID)
	if !ok {
		m.leaveTodoFocus()
		return nil
	}
	return m.todoMutateCmd(sess, op)
}

// todoMutateCmd applies one op and then reads the list back.
// Apply failure or Get failure is todoMutatedMsg. Success is todoLoadedMsg.
func (m *Model) todoMutateCmd(s domain.Session, op todoOp) tea.Cmd {
	host := s.RemoteHost
	id := s.ID
	remote, hasRemote := resolveRemote(m.cfg, s)
	client := m.todoClient
	return func() tea.Msg {
		if !hasRemote {
			return todoMutatedMsg{host: host, sessionID: id, err: fmt.Errorf("no remote for %s", host)}
		}
		if client == nil {
			return todoMutatedMsg{host: host, sessionID: id, err: errors.New("todo client is not configured")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := client.Apply(ctx, remote, op); err != nil {
			return todoMutatedMsg{host: host, sessionID: id, err: err}
		}
		snap, err := client.Get(ctx, remote, id)
		if err != nil {
			return todoMutatedMsg{host: host, sessionID: id, err: err}
		}
		return todoLoadedMsg{host: host, sessionID: id, snap: snap}
	}
}

func (m *Model) applyTodoMutated(msg todoMutatedMsg) tea.Cmd {
	if msg.err == nil {
		return nil
	}
	m.log("to-do %s on %s: %v", msg.sessionID, msg.host, msg.err)
	return m.showToast("⚠️  To-do not saved: "+msg.err.Error(), true, 3*time.Second)
}

func (m *Model) focusedTodoItems() []domain.TodoItem {
	if m.todos == nil {
		return nil
	}
	return m.todos[m.focusedSessionID].List.Items
}

func (m *Model) todoItemAtCursor() (domain.TodoItem, bool) {
	items := m.focusedTodoItems()
	if m.todoCursor < 0 || m.todoCursor >= len(items) {
		return domain.TodoItem{}, false
	}
	return items[m.todoCursor], true
}

func (m *Model) todoItemByID(id string) (domain.TodoItem, bool) {
	for _, it := range m.focusedTodoItems() {
		if it.ID == id {
			return it, true
		}
	}
	return domain.TodoItem{}, false
}

func (m *Model) moveTodoCursor(delta int) {
	n := len(m.focusedTodoItems())
	if n == 0 {
		m.todoCursor = 0
		return
	}
	next := m.todoCursor + delta
	if next < 0 {
		next = 0
	}
	if next >= n {
		next = n - 1
	}
	m.todoCursor = next
}

// todoCursorFollowID is the item the cursor is on, so a refreshed list can
// put the cursor back on that row instead of a stale index.
func (m *Model) todoCursorFollowID(sessionID string) string {
	if m.focus != focusTodos || sessionID != m.focusedSessionID {
		return ""
	}
	it, ok := m.todoItemAtCursor()
	if !ok {
		return ""
	}
	return it.ID
}

func (m *Model) restoreTodoCursor(sessionID, id string) {
	if id == "" || m.focus != focusTodos || sessionID != m.focusedSessionID {
		return
	}
	items := m.focusedTodoItems()
	for i, it := range items {
		if it.ID == id {
			m.todoCursor = i
			return
		}
	}
	if len(items) == 0 {
		m.todoCursor = 0
		return
	}
	if m.todoCursor >= len(items) {
		m.todoCursor = len(items) - 1
	}
}

// todoEditLine is the rendered add or edit field, empty when no row is open.
func (m *Model) todoEditLine(width int) string {
	if m.todoEdit == nil {
		return ""
	}
	m.ensureTodoInput()
	if width > 0 {
		m.todoInput.SetWidth(width)
	}
	return strings.TrimRight(m.todoInput.View(), "\n")
}
