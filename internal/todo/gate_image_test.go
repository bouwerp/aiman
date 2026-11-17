package todo

import (
	"errors"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

const testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestImageGateAddedBeforeDone(t *testing.T) {
	l := domain.TodoList{Items: []domain.TodoItem{{ID: "t1", Text: "work"}, {ID: "t2", Text: DoneItemText, Ref: DoneRef}}}
	if !AddImageGate(&l, testDigest, "Image approved: redis:7 (0123456789ab)") {
		t.Fatal("gate not added")
	}
	if AddImageGate(&l, testDigest, "again") {
		t.Fatal("a second gate for one digest was added")
	}
	if l.Items[1].Ref != ImageGateRef(testDigest) || l.Items[1].State != domain.TodoPending || l.Items[2].Ref != DoneRef {
		t.Fatalf("items = %+v", l.Items)
	}
	if !GateOpen(l, ImageGateRef(testDigest)) || !ServeOnlyGate(ImageGateRef(testDigest)) {
		t.Fatal("the image gate is not an open, serve-only gate")
	}
	if d, ok := ImageGateDigest(l.Items[1].Ref); !ok || d != testDigest {
		t.Fatalf("ImageGateDigest = %q, %v", d, ok)
	}
}

func TestImageGateGuarded(t *testing.T) {
	prev := domain.TodoList{}
	AddImageGate(&prev, testDigest, "Image approved: redis:7 (0123456789ab)")
	prev.Items[0].ID = "t1"
	for name, change := range map[string]func(*domain.TodoList){
		"close":  func(l *domain.TodoList) { l.Items[0].State = domain.TodoDone },
		"rename": func(l *domain.TodoList) { l.Items[0].Text = "Image approved: anything" },
		"remove": func(l *domain.TodoList) { l.Items = nil },
	} {
		next := domain.TodoList{Items: append([]domain.TodoItem(nil), prev.Items...)}
		change(&next)
		if err := GuardGate(prev, next); !errors.Is(err, ErrGateLocked) {
			t.Errorf("%s: GuardGate = %v", name, err)
		}
	}
	merged := domain.TodoList{Items: append([]domain.TodoItem(nil), prev.Items...)}
	merged.Items[0].State = domain.TodoDone
	if err := ClampGate(prev, &merged); err != nil || merged.Items[0].State != domain.TodoPending {
		t.Fatalf("ClampGate = %v, %+v", err, merged.Items[0])
	}
}

func TestClosedImageGates(t *testing.T) {
	prev := domain.TodoList{}
	AddImageGate(&prev, testDigest, "Image approved: a")
	prev.Items[0].ID = "t1"
	next := domain.TodoList{Items: append([]domain.TodoItem(nil), prev.Items...)}
	next.Items[0].State = domain.TodoDone
	if got := ClosedImageGates(prev, next); len(got) != 1 || got[0] != testDigest {
		t.Fatalf("ClosedImageGates = %q", got)
	}
	if got := ClosedImageGates(prev, prev); len(got) != 0 {
		t.Fatalf("unchanged list reports %q", got)
	}
}
