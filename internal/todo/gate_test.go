package todo

import (
	"errors"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func gateList() domain.TodoList {
	return domain.TodoList{Items: []domain.TodoItem{
		{ID: "t1", Text: "a", State: domain.TodoPending},
		{ID: "g", Text: DesignGateText, State: domain.TodoPending, Ref: DesignGateRef},
		{ID: "d", Text: DoneItemText, State: domain.TodoPending, Ref: DoneRef},
	}}
}

func TestAddGateOnce(t *testing.T) {
	l := domain.TodoList{Items: []domain.TodoItem{
		{Text: "a", State: domain.TodoPending},
		{Text: DoneItemText, State: domain.TodoPending, Ref: DoneRef},
	}}
	if !AddGate(&l) {
		t.Fatal("first call must add")
	}
	if AddGate(&l) {
		t.Fatal("second call must not add")
	}
	if len(l.Items) != 3 || l.Items[1].Ref != DesignGateRef || l.Items[2].Ref != DoneRef {
		t.Fatalf("items = %+v", l.Items)
	}
	if !GateOpen(l) {
		t.Fatal("gate must be open")
	}
}

func TestGuardGateRefusesBypass(t *testing.T) {
	tests := []struct {
		name string
		edit func(l *domain.TodoList)
		bad  bool
	}{
		{"tick done", func(l *domain.TodoList) { l.Items[1].State = domain.TodoDone }, true},
		{"drop", func(l *domain.TodoList) { l.Items[1].State = domain.TodoDropped }, true},
		{"remove", func(l *domain.TodoList) { l.Items = append(l.Items[:1], l.Items[2:]...) }, true},
		{"edit text", func(l *domain.TodoList) { l.Items[1].Text = "ok" }, true},
		{"change ref", func(l *domain.TodoList) { l.Items[1].Ref = "x" }, true},
		{"start", func(l *domain.TodoList) { l.Items[1].State = domain.TodoActive }, false},
		{"reorder", func(l *domain.TodoList) { l.Items[0], l.Items[1] = l.Items[1], l.Items[0] }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prev, next := gateList(), gateList()
			tc.edit(&next)
			err := GuardGate(prev, next)
			if tc.bad != (err != nil) {
				t.Fatalf("err = %v", err)
			}
			if tc.bad && (!errors.Is(err, ErrInvalid) || !errors.Is(err, ErrGateLocked)) {
				t.Fatalf("err does not wrap: %v", err)
			}
		})
	}
	noGate := domain.TodoList{Items: []domain.TodoItem{{ID: "t1", Text: "a", State: domain.TodoPending}}}
	if err := GuardGate(noGate, domain.TodoList{}); err != nil {
		t.Fatal(err)
	}
}
