package ui

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/server"
)

func orderedStates(states ...domain.TodoState) []domain.TodoItem {
	items := make([]domain.TodoItem, len(states))
	for i, st := range states {
		id := "t" + strconv.Itoa(i)
		items[i] = domain.TodoItem{ID: id, Text: "item " + id, State: st}
	}
	return items
}

func TestTodoDisplayOrder(t *testing.T) {
	const (
		d = domain.TodoDone
		a = domain.TodoActive
		p = domain.TodoPending
		x = domain.TodoDropped
	)
	tests := []struct {
		name   string
		states []domain.TodoState
		want   []int
	}{
		{"done first in storage", []domain.TodoState{d, d, p, a, x, p}, []int{3, 2, 5, 0, 1, 4}},
		{"no active", []domain.TodoState{x, d, p}, []int{2, 1, 0}},
		{"already ordered", []domain.TodoState{a, p, d}, []int{0, 1, 2}},
		{"empty", nil, []int{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := todoDisplayOrder(orderedStates(tc.states...))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("order = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCollapsedListShowsOpenItemsAndCountsHidden(t *testing.T) {
	states := []domain.TodoState{}
	for range 5 {
		states = append(states, domain.TodoDone)
	}
	states = append(states, domain.TodoPending, domain.TodoActive, domain.TodoPending,
		domain.TodoPending, domain.TodoPending, domain.TodoPending)
	snap := todoSnapshot{HasList: true, List: domain.TodoList{Rev: 1, Items: orderedStates(states...)}}

	lines := stripLines(renderTodoLines(todoView{Snap: snap, Cursor: -1}, 100))
	if len(lines) != maxTodoRows {
		t.Fatalf("rows = %d, want %d: %q", len(lines), maxTodoRows, lines)
	}
	if !strings.HasPrefix(lines[0], "▸ item t6") {
		t.Fatalf("first row = %q, want the active item", lines[0])
	}
	for _, l := range lines[:6] {
		if strings.HasPrefix(l, "✓") {
			t.Fatalf("done item shown ahead of an open one: %q", lines)
		}
	}
	want := "+4 more (4 done) · ctrl+t then v to expand"
	if got := lines[len(lines)-1]; got != want {
		t.Fatalf("overflow = %q, want %q", got, want)
	}

	focused := stripLines(renderTodoLines(todoView{Snap: snap, Cursor: 6}, 100))
	if got := focused[len(focused)-1]; !strings.HasSuffix(got, "v to expand") || strings.Contains(got, "ctrl+t") {
		t.Fatalf("focused overflow = %q", got)
	}
}

func TestExpandedListFitsOrScrolls(t *testing.T) {
	items := orderedStates(domain.TodoDone, domain.TodoDone, domain.TodoPending, domain.TodoActive,
		domain.TodoPending, domain.TodoPending, domain.TodoPending, domain.TodoPending,
		domain.TodoPending, domain.TodoPending, domain.TodoPending, domain.TodoDropped)
	snap := todoSnapshot{HasList: true, List: domain.TodoList{Rev: 1, Items: items}}

	t.Run("all rows fit", func(t *testing.T) {
		lines := stripLines(renderTodoLines(todoView{Snap: snap, Cursor: -1, Expanded: true, MaxRows: 12}, 100))
		if len(lines) != 12 {
			t.Fatalf("rows = %d, want 12: %q", len(lines), lines)
		}
		if strings.Contains(strings.Join(lines, "\n"), "more") {
			t.Fatalf("unexpected overflow row: %q", lines)
		}
	})
	t.Run("scrolls to cursor", func(t *testing.T) {
		// t11 is the last display row.
		lines := stripLines(renderTodoLines(todoView{Snap: snap, Cursor: 11, Expanded: true, MaxRows: 10}, 100))
		if len(lines) != 10 {
			t.Fatalf("rows = %d, want 10: %q", len(lines), lines)
		}
		if !strings.Contains(lines[8], "item t11") {
			t.Fatalf("cursor row missing: %q", lines)
		}
		if got := lines[9]; !strings.HasPrefix(got, "+3 more") {
			t.Fatalf("overflow = %q", got)
		}
	})
	t.Run("no budget keeps the cap", func(t *testing.T) {
		lines := renderTodoLines(todoView{Snap: snap, Cursor: -1, Expanded: true}, 100)
		if len(lines) != maxTodoRows {
			t.Fatalf("rows = %d", len(lines))
		}
	})
}

func TestTodoCursorFollowsDisplayOrder(t *testing.T) {
	m, fake := todoFocusModel()
	// Stored order is t1 done, t2 active, t3 pending; display is t2, t3, t1.
	m = enterTodoFocus(t, m)
	cursorID := func() string { return m.focusedTodoItems()[m.todoCursor].ID }
	if got := cursorID(); got != "t2" {
		t.Fatalf("cursor on %q, want t2", got)
	}
	next, _ := m.Update(pressRune('j'))
	m = next.(*Model)
	if got := cursorID(); got != "t3" {
		t.Fatalf("after j cursor on %q, want t3", got)
	}
	next, _ = m.Update(pressRune('j'))
	m = next.(*Model)
	if got := cursorID(); got != "t1" {
		t.Fatalf("after jj cursor on %q, want t1", got)
	}
	next, cmd := m.Update(pressRune('s'))
	m = next.(*Model)
	runMutation(t, cmd)
	op := requireOp(t, fake)
	if op.Params["id"] != "t1" {
		t.Fatalf("op targeted %#v, want t1", op.Params)
	}
	next, _ = m.Update(pressRune('k'))
	m = next.(*Model)
	if got := cursorID(); got != "t3" {
		t.Fatalf("after k cursor on %q, want t3", got)
	}
}

func TestExpandKeyTogglesPerSessionAndSurvivesRefresh(t *testing.T) {
	m, _ := todoFocusModel()
	m = enterTodoFocus(t, m)
	next, _ := m.Update(pressRune('v'))
	m = next.(*Model)
	if !m.todoExpanded["s1"] {
		t.Fatal("v did not expand")
	}
	m.todos["s1"] = newTodoSnapshot(domain.TodoList{SessionID: "s1", Rev: 4, Items: orderedStates(domain.TodoPending)}, server.SteerStatus{})
	if !m.todoExpanded["s1"] || m.todoExpanded["s2"] {
		t.Fatal("expanded state must be per session and survive a refresh")
	}
	next, _ = m.Update(pressRune('v'))
	m = next.(*Model)
	if m.todoExpanded["s1"] {
		t.Fatal("second v did not collapse")
	}
}
