package todo

import (
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestEnsureDoneItemIdempotent(t *testing.T) {
	l := domain.TodoList{}
	if !EnsureDoneItem(&l) {
		t.Fatal("first EnsureDoneItem = false")
	}
	if EnsureDoneItem(&l) {
		t.Fatal("second EnsureDoneItem = true")
	}
	if len(l.Items) != 1 || l.Items[0].Ref != DoneRef || l.Items[0].Text != DoneItemText {
		t.Fatalf("list = %+v", l)
	}
}

func TestMutatePinsDoneItemLast(t *testing.T) {
	f := NewFiles(t.TempDir())
	if _, _, err := f.Mutate("s", domain.AuthorUser, func(l *domain.TodoList) error {
		l.Items = []domain.TodoItem{
			{Text: "A", State: domain.TodoPending},
			{Text: "B", State: domain.TodoPending},
			{Text: DoneItemText, State: domain.TodoPending, Ref: DoneRef},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	list, change, err := f.Mutate("s", domain.AuthorUser, func(l *domain.TodoList) error {
		l.Items = append(l.Items, domain.TodoItem{Text: "new", State: domain.TodoPending})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 4 || list.Items[0].Text != "A" || list.Items[2].Text != "new" || list.Items[3].Ref != DoneRef {
		t.Fatalf("order = %+v", list.Items)
	}
	for _, op := range change.Ops {
		if op.Kind == OpMove && op.ID == "t3" {
			t.Fatalf("Done item generated a move: %+v", change.Ops)
		}
	}
}

func TestPinDoneLast(t *testing.T) {
	l := domain.TodoList{Items: []domain.TodoItem{
		{ID: "done", Text: DoneItemText, Ref: DoneRef},
		{ID: "t2", Text: "work"},
	}}
	pinDoneLast(&l)
	if l.Items[0].ID != "t2" || l.Items[1].ID != "done" {
		t.Fatalf("order = %+v", l.Items)
	}
}

func TestMergeKeepsDoneItemLastWithoutMarker(t *testing.T) {
	l := domain.TodoList{Items: []domain.TodoItem{
		{ID: "t1", Text: "A", State: domain.TodoPending},
		{ID: "done", Text: DoneItemText, State: domain.TodoPending, Ref: DoneRef},
	}}
	got := Merge(l, nil, []domain.TodoSnapshotItem{{Text: "A", Status: domain.TodoPending}, {Text: "B", Status: domain.TodoPending}}, time.Time{}, time.Now())
	if len(got.Items) != 3 || got.Items[2].Ref != DoneRef {
		t.Fatalf("order = %+v", got.Items)
	}
	if len(got.NotInAgent) != 0 {
		t.Fatalf("NotInAgent = %v, want none", got.NotInAgent)
	}
}

func TestMergeTicksDoneItemFromSnapshot(t *testing.T) {
	l := domain.TodoList{Items: []domain.TodoItem{
		{ID: "done", Text: DoneItemText, State: domain.TodoPending, Ref: DoneRef},
	}}
	got := Merge(l, nil, []domain.TodoSnapshotItem{{Text: DoneItemText, Status: domain.TodoDone}}, time.Time{}, time.Now())
	if len(got.Items) != 1 || got.Items[0].State != domain.TodoDone || got.Items[0].Text != DoneItemText {
		t.Fatalf("done item = %+v", got.Items)
	}
}
