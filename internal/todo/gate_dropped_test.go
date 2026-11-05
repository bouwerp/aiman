package todo

import (
	"errors"
	"slices"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestGuardGateLocksADroppedGate(t *testing.T) {
	for _, ref := range []string{DesignGateRef, ReviewGateRef, ApproveGateRef} {
		tests := []struct {
			name string
			edit func(l *domain.TodoList)
			bad  bool
		}{
			{"tick done", func(l *domain.TodoList) { l.Items[1].State = domain.TodoDone }, true},
			{"reopen", func(l *domain.TodoList) { l.Items[1].State = domain.TodoPending }, true},
			{"rename", func(l *domain.TodoList) { l.Items[1].Text = "other" }, true},
			{"remove", func(l *domain.TodoList) { l.Items = slices.Delete(l.Items, 1, 2) }, true},
			{"move", func(l *domain.TodoList) { l.Items[0], l.Items[1] = l.Items[1], l.Items[0] }, false},
			{"edit another item", func(l *domain.TodoList) { l.Items[0].Text = "b" }, false},
		}
		for _, tc := range tests {
			t.Run(ref+"/"+tc.name, func(t *testing.T) {
				text, _ := GateText(ref)
				prev := domain.TodoList{Items: []domain.TodoItem{
					{ID: "t1", Text: "a", State: domain.TodoPending},
					{ID: "g", Text: text, State: domain.TodoDropped, Ref: ref},
				}}
				next := domain.TodoList{Items: slices.Clone(prev.Items)}
				tc.edit(&next)
				err := GuardGate(prev, next)
				if tc.bad != errors.Is(err, ErrGateLocked) {
					t.Fatalf("err = %v, want locked %v", err, tc.bad)
				}
			})
		}
	}
}
