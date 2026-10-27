package ui

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/usecase"
)

// todoLoadedMsg is the result of todo.get for one session.
type todoLoadedMsg struct {
	host      string
	sessionID string
	snap      todoSnapshot
	err       error
}

// todoSweepMsg is the result of one todo.summary call.
// A nil summaries map with a nil error is an empty sweep, not a failure.
type todoSweepMsg struct {
	host      string
	summaries map[string]todoSummary
	err       error
}

// fetchTodosCmd loads the full list for one session.
// It returns nil when this host cannot serve to-dos, a fetch for the session
// is already in flight, or the session is being created or terminated.
// The unsupported flag and the loaded message use the resolved host: a blank
// session host still falls back to the active remote, and that is the host
// a restarted stream clears.
func (m *Model) fetchTodosCmd(s domain.Session) tea.Cmd {
	if s.ID == "" || m.todoClient == nil {
		return nil
	}
	if m.todoInFlight[s.ID] || m.skipSessionPolling(s.ID) {
		return nil
	}
	remote, ok := resolveRemote(m.cfg, s)
	if !ok || m.todoUnsupported[remote.Host] {
		return nil
	}
	if m.todoInFlight == nil {
		m.todoInFlight = map[string]bool{}
	}
	m.todoInFlight[s.ID] = true
	client := m.todoClient
	host := remote.Host
	id := s.ID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		snap, err := client.Get(ctx, remote, id)
		return todoLoadedMsg{host: host, sessionID: id, snap: snap, err: err}
	}
}

// sweepTodosCmd loads progress for ids, or for every session on the host when
// ids is nil. One todo.summary call either way.
func (m *Model) sweepTodosCmd(host string, ids []string) tea.Cmd {
	if m.todoClient == nil || m.cfg == nil || m.todoUnsupported[host] {
		return nil
	}
	remote, ok := remoteByHost(m.cfg, host)
	if !ok {
		return nil
	}
	client := m.todoClient
	var sessions []string
	if ids != nil {
		sessions = append([]string(nil), ids...)
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		summaries, err := client.Summary(ctx, remote, sessions)
		return todoSweepMsg{host: host, summaries: summaries, err: err}
	}
}

// maybeSweepTodos schedules one full summary per stream, on the first event
// of any type. The event proves serve is answering.
func (m *Model) maybeSweepTodos(st *sessionStream) tea.Cmd {
	if st == nil || st.todoSwept {
		return nil
	}
	if m.todoUnsupported[st.host] {
		st.todoSwept = true
		return nil
	}
	cmd := m.sweepTodosCmd(st.host, nil)
	if cmd == nil {
		return nil
	}
	st.todoSwept = true
	return cmd
}

// applyTodoEvent handles todo_changed and steer.
// Claimed events are not session activity: the caller must not refresh the pane.
func (m *Model) applyTodoEvent(host string, ev server.SessionEvent) (tea.Cmd, bool) {
	switch ev.Type {
	case "todo_changed":
		return m.todoChangedCmd(host, ev), true
	case "steer":
		m.applySteerEvent(ev)
		return nil, true
	default:
		return nil, false
	}
}

func (m *Model) todoChangedCmd(host string, ev server.SessionEvent) tea.Cmd {
	if ev.ID == "" || ev.Rev <= m.cachedTodoRev(ev.ID) {
		return nil
	}
	if m.sessionIsSelected(ev.ID) {
		s, ok := m.sessionByID(ev.ID)
		if !ok {
			it, _ := m.selectedSessionItem()
			s = it.session
		}
		// The PR watcher changes the list when a check fails or a review
		// lands, so the selected session's PR watch is read again too.
		ci := m.fetchCIStatusCmd(s)
		if m.todoInFlight[ev.ID] {
			m.noteTodoPending(ev.ID, ev.Rev)
			return ci
		}
		return tea.Batch(m.fetchTodosCmd(s), ci)
	}
	return m.sweepTodosCmd(host, []string{ev.ID})
}

// noteTodoPending records a rev whose get was skipped because one was
// already in flight. A later load that lands short of it fetches once more.
func (m *Model) noteTodoPending(id string, rev int64) {
	if id == "" || rev <= 0 {
		return
	}
	if m.todoPendingRev == nil {
		m.todoPendingRev = map[string]int64{}
	}
	if rev > m.todoPendingRev[id] {
		m.todoPendingRev[id] = rev
	}
}

// fetchPendingTodo starts one more get when the landed rev is older than a
// rev that was skipped. The pending rev is cleared as that get starts, so a
// short answer does not retry forever. A newer skip during the get records
// the next rev.
func (m *Model) fetchPendingTodo(id string, landed int64) tea.Cmd {
	want := m.todoPendingRev[id]
	if want == 0 || want <= landed {
		delete(m.todoPendingRev, id)
		return nil
	}
	s, ok := m.sessionByID(id)
	if !ok {
		if it, sel := m.selectedSessionItem(); sel && it.session.ID == id {
			s = it.session
			ok = true
		}
	}
	if !ok {
		delete(m.todoPendingRev, id)
		return nil
	}
	delete(m.todoPendingRev, id)
	return m.fetchTodosCmd(s)
}

func (m *Model) sessionIsSelected(id string) bool {
	it, ok := m.selectedSessionItem()
	return ok && it.session.ID == id
}

// cachedTodoRev is the newest rev already stored for the session, from the
// full list or the summary. Missing state is rev 0.
func (m *Model) cachedTodoRev(id string) int64 {
	var rev int64
	if m.todos != nil {
		if snap, ok := m.todos[id]; ok && snap.List.Rev > rev {
			rev = snap.List.Rev
		}
	}
	if m.todoSummaries != nil {
		if sum, ok := m.todoSummaries[id]; ok && sum.Rev > rev {
			rev = sum.Rev
		}
	}
	return rev
}

func (m *Model) applySteerEvent(ev server.SessionEvent) {
	if ev.ID == "" {
		return
	}
	if m.todos == nil {
		m.todos = map[string]todoSnapshot{}
	}
	snap := m.todos[ev.ID]
	if ev.Queued != nil {
		snap.Steer.Queued = *ev.Queued
	}
	if at := parseEventTime(ev.DeliveredAt); !at.IsZero() {
		snap.Steer.LastDeliveredAt = at
	}
	m.todos[ev.ID] = snap
}

// mergeSteer keeps a delivery that is already newer than the snapshot a
// load or steer.send just returned. That snapshot must not raise Queued
// back over the newer event, or the prompt stays "queued" after delivery.
func mergeSteer(current, incoming server.SteerStatus) server.SteerStatus {
	out := incoming
	if !current.LastDeliveredAt.After(incoming.LastDeliveredAt) {
		return out
	}
	out.LastDeliveredAt = current.LastDeliveredAt
	if incoming.Queued > current.Queued {
		out.Queued = current.Queued
	}
	return out
}

func (m *Model) applyTodoLoaded(msg todoLoadedMsg) tea.Cmd {
	follow := m.todoCursorFollowID(msg.sessionID)
	if m.todoInFlight != nil {
		delete(m.todoInFlight, msg.sessionID)
	}
	if msg.err != nil {
		m.noteTodoRPCError(msg.host, msg.err)
		return m.fetchPendingTodo(msg.sessionID, m.cachedTodoRev(msg.sessionID))
	}
	if msg.snap.List.Rev < m.cachedTodoRev(msg.sessionID) {
		return m.fetchPendingTodo(msg.sessionID, m.cachedTodoRev(msg.sessionID))
	}
	if m.todos == nil {
		m.todos = map[string]todoSnapshot{}
	}
	prev := m.todos[msg.sessionID]
	msg.snap.Steer = mergeSteer(prev.Steer, msg.snap.Steer)
	m.todos[msg.sessionID] = msg.snap
	m.putTodoSummary(msg.sessionID, summaryFromList(msg.snap.List))
	m.refreshTodoProgress(msg.sessionID)
	m.restoreTodoCursor(msg.sessionID, follow)
	return m.fetchPendingTodo(msg.sessionID, msg.snap.List.Rev)
}

func (m *Model) applyTodoSweep(msg todoSweepMsg) {
	if msg.err != nil {
		m.noteTodoRPCError(msg.host, msg.err)
		return
	}
	for id, sum := range msg.summaries {
		if sum.Rev < m.cachedTodoRev(id) {
			continue
		}
		m.putTodoSummary(id, sum)
		m.refreshTodoProgress(id)
	}
}

func (m *Model) putTodoSummary(id string, sum todoSummary) {
	if m.todoSummaries == nil {
		m.todoSummaries = map[string]todoSummary{}
	}
	if prev, ok := m.todoSummaries[id]; ok && sum.Rev < prev.Rev {
		return
	}
	m.todoSummaries[id] = sum
}

func summaryFromList(list domain.TodoList) todoSummary {
	done, total := todoProgress(list)
	var active string
	for _, it := range list.Items {
		if it.State == domain.TodoActive {
			active = it.Text
		}
	}
	return todoSummary{Done: done, Total: total, Active: active, Rev: list.Rev}
}

// noteTodoRPCError remembers a host whose serve cannot answer to-do calls.
// An old binary and a stopped serve are not toasted: the rows simply stay blank.
func (m *Model) noteTodoRPCError(host string, err error) {
	if errors.Is(err, usecase.ErrRPCUnsupported) || errors.Is(err, usecase.ErrServeNotRunning) {
		if m.todoUnsupported == nil {
			m.todoUnsupported = map[string]bool{}
		}
		m.todoUnsupported[host] = true
		return
	}
	m.log("todos on %s: %v", host, err)
}

// refreshTodoProgress copies the cached summary onto the matching list row.
func (m *Model) refreshTodoProgress(id string) {
	sum, ok := m.todoSummaries[id]
	if !ok {
		return
	}
	for idx, it := range m.list.Items() {
		sessItem, ok := it.(item)
		if !ok || sessItem.header || sessItem.session.ID != id {
			continue
		}
		sessItem.todoDone, sessItem.todoTotal, sessItem.hasTodos = todoProgressFields(sum)
		m.list.SetItem(idx, sessItem)
		return
	}
}
