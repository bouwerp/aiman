package todo

import (
	"errors"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestApproveGateIsProtected(t *testing.T) {
	if text, ok := GateText(ApproveGateRef); !ok || text != ApproveGateText || ApproveGateRef != "gate:approve" {
		t.Fatalf("gate text: %q, %v", text, ok)
	}
	if !ServeOnlyGate(ApproveGateRef) || ServeOnlyGate(DesignGateRef) || ServeOnlyGate(ReviewGateRef) || ServeOnlyGate("") {
		t.Fatal("only gate:approve is serve-only")
	}
	var l domain.TodoList
	if !AddGate(&l, ApproveGateRef) || !GateOpen(l, ApproveGateRef) {
		t.Fatalf("add: %+v", l)
	}
	closed := domain.TodoList{Items: append([]domain.TodoItem(nil), l.Items...)}
	closed.Items[0].State = domain.TodoDone
	if err := GuardGate(l, closed); !errors.Is(err, ErrGateLocked) {
		t.Fatalf("a session must not close it: %v", err)
	}
}
