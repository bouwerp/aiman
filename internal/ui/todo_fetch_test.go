package ui

import (
	"context"
	"fmt"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/usecase"
)

// deadlineTodoClient records how long the Get context has left.
type deadlineTodoClient struct {
	*fakeTodoClient
	remain time.Duration
}

func (d *deadlineTodoClient) Get(ctx context.Context, remote config.Remote, id string) (todoSnapshot, error) {
	if dl, ok := ctx.Deadline(); ok {
		d.remain = time.Until(dl)
	}
	return d.fakeTodoClient.Get(ctx, remote, id)
}

func todoEventModel(sessions ...domain.Session) (*Model, *fakeTodoClient) {
	m := eventModel(sessions...)
	fake := &fakeTodoClient{}
	m.todoClient = fake
	if len(sessions) == 0 {
		return m, fake
	}
	items := make([]list.Item, len(sessions))
	for i, s := range sessions {
		items[i] = item{session: s}
	}
	m.list = list.New(items, list.NewDefaultDelegate(), 40, 20)
	m.list.Select(0)
	return m, fake
}

// runCmd executes a command. A batch is unpacked and each command is run.
// Callers close the stream channel first so the re-queued wait cannot block.
func runCmd(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("nil command")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	out := make([]tea.Msg, 0, len(batch))
	for _, c := range batch {
		out = append(out, c())
	}
	return out
}

func TestTodoChangedFetchesWithoutPaneRefresh(t *testing.T) {
	sess := domain.Session{ID: "p", RemoteHost: "regent0"}
	m, fake := todoEventModel(sess)
	dl := &deadlineTodoClient{fakeTodoClient: fake}
	m.todoClient = dl
	fake.snap = newTodoSnapshot(domain.TodoList{SessionID: "p", Rev: 3}, server.SteerStatus{})

	ev := server.SessionEvent{Type: "todo_changed", ID: "p", Rev: 3}
	cmd, claimed := m.applyTodoEvent("regent0", ev)
	if !claimed || cmd == nil {
		t.Fatal("a newer todo_changed for the selected session should fetch")
	}
	msgs := runCmd(t, cmd)
	if len(msgs) != 1 {
		t.Fatalf("msgs: %#v", msgs)
	}
	loaded, ok := msgs[0].(todoLoadedMsg)
	if !ok || loaded.sessionID != "p" || loaded.err != nil {
		t.Fatalf("got %#v", msgs[0])
	}
	if len(fake.gets) != 1 || fake.gets[0] != "p" {
		t.Fatalf("gets: %#v", fake.gets)
	}
	if dl.remain < 29*time.Second || dl.remain > 30*time.Second {
		t.Fatalf("get deadline %s", dl.remain)
	}
	m.applyTodoLoaded(loaded)
	if m.todoSummaries["p"].Rev != 3 {
		t.Fatalf("summary: %+v", m.todoSummaries["p"])
	}

	m.ensureSessionStreams()
	if cmd := m.applySessionEvent(sessionEventMsg{host: "regent0", event: ev}); cmd == nil {
		t.Fatal("the stream would stall")
	}
	if !m.eventSeen["p"].lastRefresh.IsZero() {
		t.Fatal("todo_changed queued a pane refresh")
	}
}

func TestTodoChangedIgnoresOldRev(t *testing.T) {
	m, fake := todoEventModel(domain.Session{ID: "p", RemoteHost: "regent0"})
	m.todos = map[string]todoSnapshot{
		"p": newTodoSnapshot(domain.TodoList{SessionID: "p", Rev: 5}, server.SteerStatus{}),
	}
	for _, rev := range []int64{4, 5} {
		cmd, claimed := m.applyTodoEvent("regent0", server.SessionEvent{Type: "todo_changed", ID: "p", Rev: rev})
		if !claimed {
			t.Fatalf("rev %d: a stale todo_changed is still a to-do event", rev)
		}
		if cmd != nil {
			t.Fatalf("rev %d: must not fetch", rev)
		}
	}
	if len(fake.gets) != 0 || len(fake.summaries) != 0 {
		t.Fatalf("gets %#v summaries %#v", fake.gets, fake.summaries)
	}
}

func TestSteerEventUpdatesStatusWithoutFetch(t *testing.T) {
	m, fake := todoEventModel(domain.Session{ID: "p", RemoteHost: "regent0"})
	zero := 0
	ev := server.SessionEvent{
		Type:        "steer",
		ID:          "p",
		Queued:      &zero,
		DeliveredAt: "2026-10-22T10:00:01Z",
	}
	cmd, claimed := m.applyTodoEvent("regent0", ev)
	if !claimed {
		t.Fatal("steer should be claimed")
	}
	if cmd != nil {
		t.Fatal("steer must not fetch")
	}
	got := m.todos["p"].Steer.LastDeliveredAt
	want := time.Date(2026, 10, 22, 10, 0, 1, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("LastDeliveredAt: %v", got)
	}
	if m.todos["p"].Steer.Queued != 0 {
		t.Fatalf("Queued: %d", m.todos["p"].Steer.Queued)
	}
	if len(fake.gets) != 0 || len(fake.summaries) != 0 {
		t.Fatalf("gets %#v summaries %#v", fake.gets, fake.summaries)
	}
}

func TestStaleTodoResultDoesNotOverwrite(t *testing.T) {
	m, _ := todoEventModel(domain.Session{ID: "p", RemoteHost: "regent0"})
	m.todos = map[string]todoSnapshot{
		"p": newTodoSnapshot(domain.TodoList{SessionID: "p", Rev: 7, Items: []domain.TodoItem{{ID: "t1", Text: "keep"}}}, server.SteerStatus{}),
	}
	m.applyTodoLoaded(todoLoadedMsg{
		host:      "regent0",
		sessionID: "p",
		snap:      newTodoSnapshot(domain.TodoList{SessionID: "p", Rev: 6, Items: []domain.TodoItem{{ID: "t2", Text: "stale"}}}, server.SteerStatus{}),
	})
	if m.todos["p"].List.Rev != 7 || m.todos["p"].List.Items[0].ID != "t1" {
		t.Fatalf("cached list overwritten: %+v", m.todos["p"].List)
	}
}

func TestOldServeMarksHostUnsupported(t *testing.T) {
	sess := domain.Session{ID: "p", RemoteHost: "regent0"}
	m, fake := todoEventModel(sess)
	fake.err = fmt.Errorf("todo.get: %w", usecase.ErrRPCUnsupported)

	cmd, claimed := m.applyTodoEvent("regent0", server.SessionEvent{Type: "todo_changed", ID: "p", Rev: 1})
	if !claimed || cmd == nil {
		t.Fatal("expected a fetch against the old serve")
	}
	msgs := runCmd(t, cmd)
	loaded, ok := msgs[0].(todoLoadedMsg)
	if !ok {
		t.Fatalf("got %#v", msgs[0])
	}
	m.applyTodoLoaded(loaded)
	if !m.todoUnsupported["regent0"] {
		t.Fatalf("unsupported: %#v", m.todoUnsupported)
	}
	if m.fetchTodosCmd(sess) != nil {
		t.Fatal("a fetch on an unsupported host must be skipped")
	}
	if m.snapshotToast != "" {
		t.Fatalf("toast %q", m.snapshotToast)
	}

	m.applyStreamEnded(sessionStreamEndedMsg{host: "regent0"})
	if m.todoUnsupported["regent0"] {
		t.Fatal("a restarted stream must clear the unsupported flag")
	}
	if m.fetchTodosCmd(sess) == nil {
		t.Fatal("fetch should resume after the stream restarts")
	}
}

func TestTodoSweepKeepsNewerSummary(t *testing.T) {
	m, _ := todoEventModel(domain.Session{ID: "p", RemoteHost: "regent0"})
	m.todoSummaries = map[string]todoSummary{"p": {Rev: 5, Done: 1, Total: 1}}
	m.applyTodoSweep(todoSweepMsg{
		host: "regent0",
		summaries: map[string]todoSummary{
			"p": {Rev: 4, Done: 0, Total: 2},
			"q": {Rev: 2, Done: 1, Total: 3, Active: "go"},
		},
	})
	if m.todoSummaries["p"].Rev != 5 || m.todoSummaries["p"].Done != 1 {
		t.Fatalf("newer summary overwritten: %+v", m.todoSummaries["p"])
	}
	if m.todoSummaries["q"] != (todoSummary{Done: 1, Total: 3, Active: "go", Rev: 2}) {
		t.Fatalf("q: %+v", m.todoSummaries["q"])
	}
}

func TestTodoChangedForOtherSessionFetchesSummary(t *testing.T) {
	m, fake := todoEventModel(
		domain.Session{ID: "p", RemoteHost: "regent0"},
		domain.Session{ID: "q", RemoteHost: "regent0"},
	)
	fake.summary = map[string]todoSummary{"q": {Done: 1, Total: 2, Rev: 4}}
	cmd, claimed := m.applyTodoEvent("regent0", server.SessionEvent{Type: "todo_changed", ID: "q", Rev: 4})
	if !claimed || cmd == nil {
		t.Fatal("a non-selected todo_changed should fetch a summary")
	}
	msgs := runCmd(t, cmd)
	if _, ok := msgs[0].(todoSweepMsg); !ok {
		t.Fatalf("got %#v", msgs[0])
	}
	if len(fake.gets) != 0 {
		t.Fatalf("Get recorded: %#v", fake.gets)
	}
	if len(fake.summaries) != 1 || len(fake.summaries[0]) != 1 || fake.summaries[0][0] != "q" {
		t.Fatalf("summaries: %#v", fake.summaries)
	}
}

func TestFirstEventSweepsOnce(t *testing.T) {
	m, fake := todoEventModel(domain.Session{ID: "p", RemoteHost: "regent0"})
	m.ensureSessionStreams()
	close(m.streams["regent0"].events)

	ev := server.SessionEvent{Type: "keepalive"}
	for range 2 {
		cmd := m.applySessionEvent(sessionEventMsg{host: "regent0", event: ev})
		runCmd(t, cmd)
	}
	if len(fake.summaries) != 1 || fake.summaries[0] != nil {
		t.Fatalf("summaries: %#v", fake.summaries)
	}
	if len(fake.gets) != 0 {
		t.Fatalf("gets: %#v", fake.gets)
	}
}

func TestTodoChangedDuringFetchIsNotDropped(t *testing.T) {
	sess := domain.Session{ID: "p", RemoteHost: "regent0"}
	m, fake := todoEventModel(sess)
	m.todoInFlight = map[string]bool{sess.ID: true}
	fake.snap = newTodoSnapshot(domain.TodoList{SessionID: "p", Rev: 2}, server.SteerStatus{})

	cmd, claimed := m.applyTodoEvent("regent0", server.SessionEvent{Type: "todo_changed", ID: "p", Rev: 4})
	if !claimed {
		t.Fatal("todo_changed is still claimed while a get is in flight")
	}
	if cmd != nil {
		t.Fatal("an in-flight get must not start a second one")
	}
	if len(fake.gets) != 0 {
		t.Fatalf("gets %#v", fake.gets)
	}

	fake.snap = newTodoSnapshot(domain.TodoList{SessionID: "p", Rev: 4}, server.SteerStatus{})
	var next tea.Model
	next, cmd = m.Update(todoLoadedMsg{
		host:      "regent0",
		sessionID: "p",
		snap:      newTodoSnapshot(domain.TodoList{SessionID: "p", Rev: 2}, server.SteerStatus{}),
	})
	m = next.(*Model)
	if cmd == nil {
		t.Fatal("a load short of the skipped rev must fetch once more")
	}
	msgs := runCmd(t, cmd)
	loaded, ok := msgs[0].(todoLoadedMsg)
	if !ok || loaded.sessionID != "p" {
		t.Fatalf("follow-up %#v", msgs)
	}
	if len(fake.gets) != 1 {
		t.Fatalf("gets %#v", fake.gets)
	}
	next, cmd = m.Update(loaded)
	m = next.(*Model)
	if cmd != nil {
		t.Fatal("the list caught up; no further fetch")
	}
	if m.todos["p"].List.Rev != 4 {
		t.Fatalf("rev %d", m.todos["p"].List.Rev)
	}
}

func TestLoadKeepsNewerSteer(t *testing.T) {
	m, _ := todoEventModel(domain.Session{ID: "p", RemoteHost: "regent0"})
	delivered := time.Date(2026, 10, 22, 10, 0, 5, 0, time.UTC)
	m.todos = map[string]todoSnapshot{
		"p": newTodoSnapshot(domain.TodoList{SessionID: "p", Rev: 2}, server.SteerStatus{
			Queued:          0,
			LastDeliveredAt: delivered,
		}),
	}
	m.applyTodoLoaded(todoLoadedMsg{
		host:      "regent0",
		sessionID: "p",
		snap: newTodoSnapshot(domain.TodoList{
			SessionID: "p",
			Rev:       3,
			Items:     []domain.TodoItem{{ID: "t1", Text: "next"}},
		}, server.SteerStatus{
			Queued:          1,
			LastDeliveredAt: delivered.Add(-time.Minute),
		}),
	})
	got := m.todos["p"].Steer
	if !got.LastDeliveredAt.Equal(delivered) {
		t.Fatalf("LastDeliveredAt %s", got.LastDeliveredAt)
	}
	if got.Queued != 0 {
		t.Fatalf("Queued %d", got.Queued)
	}
	if m.todos["p"].List.Rev != 3 {
		t.Fatalf("list rev %d", m.todos["p"].List.Rev)
	}
}

func TestBlankHostUnsupportedUsesResolvedHost(t *testing.T) {
	sess := domain.Session{ID: "p"}
	m, fake := todoEventModel(sess)
	fake.err = fmt.Errorf("todo.get: %w", usecase.ErrRPCUnsupported)

	cmd := m.fetchTodosCmd(sess)
	if cmd == nil {
		t.Fatal("empty RemoteHost still resolves a remote")
	}
	msgs := runCmd(t, cmd)
	loaded, ok := msgs[0].(todoLoadedMsg)
	if !ok {
		t.Fatalf("got %#v", msgs[0])
	}
	if loaded.host != "regent0" {
		t.Fatalf("host %q", loaded.host)
	}
	m.applyTodoLoaded(loaded)
	if !m.todoUnsupported["regent0"] {
		t.Fatalf("unsupported %#v", m.todoUnsupported)
	}
	if m.todoUnsupported[""] {
		t.Fatal("empty host must not be the unsupported key")
	}
	m.applyStreamEnded(sessionStreamEndedMsg{host: "regent0"})
	if m.todoUnsupported["regent0"] {
		t.Fatal("stream restart must clear the resolved host")
	}
	if m.fetchTodosCmd(sess) == nil {
		t.Fatal("fetch should resume after the stream restarts")
	}
}

func TestSelectionChangeFetchesTodos(t *testing.T) {
	now := time.Now()
	first := domain.Session{
		ID: "a", Name: "first", Group: "G", RemoteHost: "regent0",
		TmuxSession: "a", CreatedAt: now,
	}
	second := domain.Session{
		ID: "b", Name: "second", Group: "G", RemoteHost: "regent0",
		TmuxSession: "b", CreatedAt: now.Add(-time.Minute),
	}
	cfg := &config.Config{Remotes: []config.Remote{{Host: "regent0", User: "code"}}}
	m := NewModel(cfg, nil, []domain.Session{first, second}, nil, nil, nil, nil)
	m.todoClient = &fakeTodoClient{}
	m.SetSize(140, 44)
	m.selectFirstSessionRow()
	sel, ok := m.selectedSessionItem()
	if !ok || sel.session.ID != first.ID {
		t.Fatalf("selected %#v", sel.session.ID)
	}
	if _, cmd := m.Update(pressKey("down")); cmd == nil {
		t.Fatal("selection change returned no command")
	}
	if !m.todoInFlight[second.ID] {
		t.Fatalf("in flight: %#v", m.todoInFlight)
	}
}
