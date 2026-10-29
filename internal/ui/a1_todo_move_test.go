package ui

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/server"
)

func TestMoveSwapsWithDisplayedNeighbour(t *testing.T) {
	// Stored: t0 t1 done, t2 t3 t4 pending. Displayed: t2 t3 t4 t0 t1.
	tests := []struct {
		name   string
		cursor int
		key    rune
		id     string
		to     int
		wantOp bool
	}{
		{"K swaps with the pending above", 3, 'K', "t3", 2, true},
		{"J swaps with the pending below", 3, 'J', "t3", 4, true},
		{"K at the displayed top", 2, 'K', "", 0, false},
		{"J at the displayed bottom", 1, 'J', "", 0, false},
		{"J onto a done item", 4, 'J', "t4", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, fake := todoFocusModel()
			list := domain.TodoList{SessionID: "s1", Rev: 1, Items: orderedStates(
				domain.TodoDone, domain.TodoDone, domain.TodoPending, domain.TodoPending, domain.TodoPending)}
			m.todos["s1"] = newTodoSnapshot(list, server.SteerStatus{})
			m = enterTodoFocus(t, m)
			m.todoCursor = tc.cursor
			_, cmd := m.Update(pressRune(tc.key))
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
			if op.Method != "todo.move" || op.Params["id"] != tc.id || op.Params["to_index"] != tc.to {
				t.Fatalf("op = %+v, want move %s to %d", op, tc.id, tc.to)
			}
		})
	}
}
