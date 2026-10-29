package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/server"
)

func todoFocusModel() (*Model, *fakeTodoClient) {
	sess := domain.Session{ID: "s1", IssueKey: "AIM-1", Name: "impl", RemoteHost: "regent0"}
	m, fake := promptModel(sess)
	snap := newTodoSnapshot(domain.TodoList{
		SessionID: "s1",
		Rev:       3,
		Items: []domain.TodoItem{
			{ID: "t1", Text: "Ship the fix", State: domain.TodoDone},
			{ID: "t2", Text: "Write the test", State: domain.TodoActive},
			{ID: "t3", Text: "Open the PR", State: domain.TodoPending},
		},
	}, server.SteerStatus{})
	m.todos = map[string]todoSnapshot{sess.ID: snap}
	// Get after a successful Apply returns the same list. The slice is not
	// shared with the model, so a local reorder cannot change what Get answers.
	fake.snap = newTodoSnapshot(domain.TodoList{
		SessionID: snap.List.SessionID,
		Rev:       snap.List.Rev,
		Items:     append([]domain.TodoItem(nil), snap.List.Items...),
	}, server.SteerStatus{})
	m.daemonList = list.New(nil, list.NewDefaultDelegate(), 20, 10)
	m.daemons = map[string]domain.Daemon{}
	return m, fake
}

func enterTodoFocus(t *testing.T, m *Model) *Model {
	t.Helper()
	next, _ := m.Update(pressKey("ctrl+t"))
	got := next.(*Model)
	if got.focus != focusTodos {
		t.Fatalf("focus = %v, want focusTodos", got.focus)
	}
	return got
}

func runMutation(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a mutation command")
	}
	_ = cmd()
}

func requireOp(t *testing.T, fake *fakeTodoClient) todoOp {
	t.Helper()
	if len(fake.ops) != 1 {
		t.Fatalf("ops = %+v, want 1", fake.ops)
	}
	op := fake.ops[0]
	if _, ok := op.Params["as"]; ok {
		t.Fatalf("op includes as: %+v", op.Params)
	}
	if op.Params["session"] != "s1" {
		t.Fatalf("session = %#v", op.Params["session"])
	}
	return op
}

func TestCtrlTEntersTodoFocusOnActiveItem(t *testing.T) {
	m, _ := todoFocusModel()
	m = enterTodoFocus(t, m)
	if m.focusedSessionID != "s1" {
		t.Fatalf("focusedSessionID = %q", m.focusedSessionID)
	}
	if m.todoCursor != 1 {
		t.Fatalf("cursor = %d, want 1 (the active item)", m.todoCursor)
	}
}

func TestTodoKeysSendOps(t *testing.T) {
	// Params["state"] is the wire string. A domain.TodoState compare is false
	// even when the value is the right word.
	to2 := 2
	to1 := 1
	cases := []struct {
		name    string
		keys    []string
		method  string
		id      string
		state   string
		toIndex *int
		wantOp  bool
	}{
		{name: "space on t3", keys: []string{"down", "space"}, method: "todo.update", id: "t3", state: "done", wantOp: true},
		{name: "space on t1", keys: []string{"down", "down", "space"}, method: "todo.update", id: "t1", state: "pending", wantOp: true},
		{name: "s on t3", keys: []string{"down", "s"}, method: "todo.update", id: "t3", state: "active", wantOp: true},
		{name: "d", keys: []string{"d"}, method: "todo.update", id: "t2", state: "dropped", wantOp: true},
		{name: "x", keys: []string{"x"}, method: "todo.remove", id: "t2", wantOp: true},
		{name: "J on t2", keys: []string{"J"}, method: "todo.move", id: "t2", toIndex: &to2, wantOp: true},
		{name: "K on t2 is the top row", keys: []string{"K"}, wantOp: false},
		{name: "K on t3", keys: []string{"down", "K"}, method: "todo.move", id: "t3", toIndex: &to1, wantOp: true},
		{name: "J on t1 is the bottom row", keys: []string{"down", "down", "J"}, wantOp: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, fake := todoFocusModel()
			m = enterTodoFocus(t, m)
			var cmd tea.Cmd
			for _, key := range tc.keys {
				var next tea.Model
				next, cmd = m.Update(pressKey(key))
				m = next.(*Model)
			}
			if !tc.wantOp {
				if cmd != nil {
					_ = cmd()
				}
				if len(fake.ops) != 0 {
					t.Fatalf("ops = %+v, want none", fake.ops)
				}
				return
			}
			runMutation(t, cmd)
			op := requireOp(t, fake)
			if op.Method != tc.method {
				t.Fatalf("method = %s, want %s", op.Method, tc.method)
			}
			if op.Params["id"] != tc.id {
				t.Fatalf("id = %#v, want %s", op.Params["id"], tc.id)
			}
			if tc.state != "" && op.Params["state"] != tc.state {
				t.Fatalf("state = %#v (%T), want %q", op.Params["state"], op.Params["state"], tc.state)
			}
			if tc.toIndex != nil && op.Params["to_index"] != *tc.toIndex {
				t.Fatalf("to_index = %#v, want %d", op.Params["to_index"], *tc.toIndex)
			}
		})
	}
}

func TestTodoMoveCursorFollowsItem(t *testing.T) {
	m, fake := todoFocusModel()
	m = enterTodoFocus(t, m)
	moved := append([]domain.TodoItem(nil), fake.snap.List.Items...)
	moved[1], moved[2] = moved[2], moved[1]
	fake.snap.List.Items = moved

	next, cmd := m.Update(pressKey("J"))
	m = next.(*Model)
	if cmd == nil {
		t.Fatal("expected a move command")
	}
	next, _ = m.Update(cmd())
	m = next.(*Model)
	if m.todoCursor != 2 {
		t.Fatalf("cursor = %d, want 2", m.todoCursor)
	}
	if got := m.focusedTodoItems()[m.todoCursor].ID; got != "t2" {
		t.Fatalf("cursor item = %s, want t2", got)
	}
}

func TestTodoAddAndEdit(t *testing.T) {
	m, fake := todoFocusModel()
	m = enterTodoFocus(t, m)

	var next tea.Model
	var cmd tea.Cmd
	next, _ = m.Update(pressRune('a'))
	m = next.(*Model)
	next, _ = m.Update(pressKey("Write runbook"))
	m = next.(*Model)
	next, cmd = m.Update(pressKey("enter"))
	m = next.(*Model)
	runMutation(t, cmd)
	op := requireOp(t, fake)
	if op.Method != "todo.add" || op.Params["text"] != "Write runbook" || op.Params["after"] != "t2" {
		t.Fatalf("add = %+v", op)
	}

	next, _ = m.Update(pressKey("down"))
	m = next.(*Model)
	next, _ = m.Update(pressRune('e'))
	m = next.(*Model)
	next, _ = m.Update(pressKey("ctrl+u"))
	m = next.(*Model)
	next, _ = m.Update(pressKey("Open PR now"))
	m = next.(*Model)
	next, cmd = m.Update(pressKey("enter"))
	m = next.(*Model)
	runMutation(t, cmd)
	if len(fake.ops) != 2 {
		t.Fatalf("ops = %+v", fake.ops)
	}
	edit := fake.ops[1]
	if edit.Method != "todo.update" || edit.Params["id"] != "t3" || edit.Params["text"] != "Open PR now" {
		t.Fatalf("edit = %+v", edit)
	}
	if _, ok := edit.Params["as"]; ok {
		t.Fatalf("edit includes as: %+v", edit.Params)
	}

	before := len(fake.ops)
	next, _ = m.Update(pressRune('e'))
	m = next.(*Model)
	next, cmd = m.Update(pressKey("esc"))
	m = next.(*Model)
	if cmd != nil {
		_ = cmd()
	}
	if len(fake.ops) != before {
		t.Fatalf("esc during edit recorded %+v", fake.ops[before:])
	}
	if m.focus != focusTodos {
		t.Fatalf("focus after cancel = %v, want focusTodos", m.focus)
	}
}

func TestTodoFocusEscReturnsToList(t *testing.T) {
	m, fake := todoFocusModel()
	m = enterTodoFocus(t, m)
	next, cmd := m.Update(pressKey("esc"))
	m = next.(*Model)
	if cmd != nil {
		_ = cmd()
	}
	if m.focus != focusList {
		t.Fatalf("focus = %v, want focusList", m.focus)
	}
	if len(fake.ops) != 0 {
		t.Fatalf("ops = %+v", fake.ops)
	}
}

func TestTodoFocusDoesNotAttach(t *testing.T) {
	m, _ := todoFocusModel()
	m = enterTodoFocus(t, m)
	next, cmd := m.Update(pressRune('a'))
	m = next.(*Model)
	if m.state != viewStateMain {
		t.Fatalf("state = %v, want viewStateMain", m.state)
	}
	if cmd != nil {
		if _, ok := cmd().(attachMsg); ok {
			t.Fatal("a in to-do focus yielded attachMsg")
		}
	}
}

func TestTodoFocusDropsWhenSessionGone(t *testing.T) {
	m, fake := todoFocusModel()
	m = enterTodoFocus(t, m)
	m.allSessions = nil
	m.applyRemoteFilter()
	next, cmd := m.Update(pressKey("space"))
	m = next.(*Model)
	if cmd != nil {
		_ = cmd()
	}
	if m.focus != focusList {
		t.Fatalf("focus = %v, want focusList", m.focus)
	}
	if len(fake.ops) != 0 {
		t.Fatalf("ops = %+v", fake.ops)
	}
}

type splitDeadlineClient struct {
	applyDL time.Time
	getDL   time.Time
	getErr  error
	snap    todoSnapshot
}

func (c *splitDeadlineClient) Get(ctx context.Context, _ config.Remote, _ string) (todoSnapshot, error) {
	if dl, ok := ctx.Deadline(); ok {
		c.getDL = dl
	}
	if c.getErr != nil {
		return todoSnapshot{}, c.getErr
	}
	return c.snap, nil
}

func (c *splitDeadlineClient) Summary(context.Context, config.Remote, []string) (map[string]todoSummary, error) {
	return nil, nil
}

func (c *splitDeadlineClient) Apply(ctx context.Context, _ config.Remote, _ todoOp) error {
	if dl, ok := ctx.Deadline(); ok {
		c.applyDL = dl
	}
	return nil
}

func (c *splitDeadlineClient) Prompt(context.Context, config.Remote, string, string) (server.SteerStatus, error) {
	return server.SteerStatus{}, nil
}

func TestTodoMutateGetHasItsOwnDeadline(t *testing.T) {
	m, _ := todoFocusModel()
	c := &splitDeadlineClient{getErr: errors.New("slow")}
	m.todoClient = c
	m = enterTodoFocus(t, m)
	next, cmd := m.Update(pressKey("s"))
	m = next.(*Model)
	if cmd == nil {
		t.Fatal("expected a mutation command")
	}
	msg := cmd()
	if c.applyDL.IsZero() || c.getDL.IsZero() {
		t.Fatal("Apply and Get must both carry a deadline")
	}
	if !c.getDL.After(c.applyDL) {
		t.Fatalf("Get reused Apply's deadline: apply %s get %s", c.applyDL, c.getDL)
	}
	mutated, ok := msg.(todoMutatedMsg)
	if !ok {
		t.Fatalf("got %T", msg)
	}
	next, _ = m.Update(mutated)
	m = next.(*Model)
	if strings.Contains(m.snapshotToast, "not saved") {
		t.Fatalf("toast claims the edit was lost: %q", m.snapshotToast)
	}
	if !strings.Contains(m.snapshotToast, "could not be reloaded") {
		t.Fatalf("toast %q", m.snapshotToast)
	}
}

func TestSpaceOnDroppedSetsPending(t *testing.T) {
	m, fake := todoFocusModel()
	list := m.todos["s1"].List
	list.Items = append(list.Items, domain.TodoItem{ID: "t4", Text: "skip", State: domain.TodoDropped})
	snap := m.todos["s1"]
	snap.List = list
	m.todos["s1"] = snap
	m = enterTodoFocus(t, m)
	var cmd tea.Cmd
	for _, key := range []string{"down", "down", "down", "space"} {
		var next tea.Model
		next, cmd = m.Update(pressKey(key))
		m = next.(*Model)
	}
	runMutation(t, cmd)
	op := requireOp(t, fake)
	if op.Params["id"] != "t4" || op.Params["state"] != string(domain.TodoPending) {
		t.Fatalf("params %+v", op.Params)
	}
}

func TestTodoFocusMutationErrorToasts(t *testing.T) {
	m, fake := todoFocusModel()
	fake.err = errors.New("offline")
	m = enterTodoFocus(t, m)
	next, cmd := m.Update(pressKey("s"))
	m = next.(*Model)
	if cmd == nil {
		t.Fatal("expected a mutation command")
	}
	next, toast := m.Update(cmd())
	m = next.(*Model)
	const want = "⚠️  To-do not saved: offline"
	if m.snapshotToast != want {
		t.Fatalf("toast = %q, want %q", m.snapshotToast, want)
	}
	if toast == nil {
		t.Fatal("expected the toast to clear")
	}
	if len(fake.gets) != 0 {
		t.Fatalf("Get after a failed Apply: %v", fake.gets)
	}
}
