package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/todo"
)

// gateServer has an owner session "own" with an open gate item and a second
// session "other".
func gateServer(t *testing.T) *Server {
	t.Helper()
	srv, _, _ := todoServer(t, todoSession("own"), todoSession("other"))
	var res todoListResult
	rpcOK(t, srv, "", "todo.gate", map[string]any{"session": "own"}, &res)
	return srv
}

func gateID(t *testing.T, srv *Server) string {
	t.Helper()
	list, err := srv.todos.Get("own")
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range list.Items {
		if it.Ref == "gate:design" {
			return it.ID
		}
	}
	t.Fatal("no gate item")
	return ""
}

func wantCode(t *testing.T, resp Response, code string) {
	t.Helper()
	if resp.Error == nil || resp.Error.Code != code {
		t.Fatalf("response = %+v, want code %s", resp.Error, code)
	}
}

func TestGateRefusesSessionCallers(t *testing.T) {
	srv := gateServer(t)
	id := gateID(t, srv)
	for _, caller := range []string{"own", "other"} {
		wantCode(t, rpc(t, srv, caller, "todo.update", map[string]any{"session": "own", "id": id, "state": "done"}), CodeGateNeedsOperator)
		wantCode(t, rpc(t, srv, caller, "todo.update", map[string]any{"session": "own", "id": id, "state": "dropped"}), CodeGateNeedsOperator)
		wantCode(t, rpc(t, srv, caller, "todo.remove", map[string]any{"session": "own", "id": id}), CodeGateNeedsOperator)
	}
}

func TestGateOperatorTicks(t *testing.T) {
	srv := gateServer(t)
	id := gateID(t, srv)
	rpcOK(t, srv, "", "todo.update", map[string]any{"session": "own", "id": id, "state": "done"}, nil)
}

func TestApproveDesign(t *testing.T) {
	srv := gateServer(t)
	wantCode(t, rpc(t, srv, "own", "todo.approve_design", map[string]any{"session": "own", "quote": "  "}), CodeInvalidParams)
	wantCode(t, rpc(t, srv, "other", "todo.approve_design", map[string]any{"session": "own", "quote": "go ahead"}), CodeGateNeedsOperator)
	rpcOK(t, srv, "own", "todo.approve_design", map[string]any{"session": "own", "quote": "go ahead"}, nil)
	list, _ := srv.todos.Get("own")
	for _, it := range list.Items {
		if it.Ref == "gate:design" && it.State != domain.TodoDone {
			t.Fatalf("gate state %s", it.State)
		}
	}
	rows, err := srv.repo.(sessionEventLister).ListSessionEvents(context.Background(), "own", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range rows {
		if r.Kind == domain.EventDesignApproved {
			n++
			if string(r.Payload) != `{"quote":"go ahead"}` {
				t.Fatalf("payload %s", r.Payload)
			}
		}
	}
	if n != 1 {
		t.Fatalf("design.approved rows = %d", n)
	}
}

func TestForBlockedByOpenGate(t *testing.T) {
	srv, creator := forServer(t, "")
	create := func() Response {
		return rpc(t, srv, "", "session.create", map[string]any{"for": "t3", "parent": "p"})
	}
	// The seeded items carry explicit ids, so give the gate one too.
	err := srv.TodoAccess().Mutate(context.Background(), "p", domain.AuthorAiman, func(l *domain.TodoList) error {
		todo.AddGate(l, todo.DesignGateRef)
		for i := range l.Items {
			if l.Items[i].Ref == todo.DesignGateRef {
				l.Items[i].ID = "g1"
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCode(t, create(), CodeDesignNotApproved)
	if creator.calls != 0 {
		t.Fatalf("created %d sessions while the gate was open", creator.calls)
	}
	rpcOK(t, srv, "p", "todo.approve_design", map[string]any{"session": "p", "quote": "ship it"}, nil)
	if resp := create(); resp.Error != nil {
		t.Fatalf("create after approval: %+v", resp.Error)
	}
}

func TestForAllowedWithoutGate(t *testing.T) {
	srv, _ := forServer(t, "")
	if resp := rpc(t, srv, "", "session.create", map[string]any{"for": "t3", "parent": "p"}); resp.Error != nil {
		t.Fatalf("create: %+v", resp.Error)
	}
}

func TestMirrorCannotCloseGate(t *testing.T) {
	srv := gateServer(t)
	resp := rpc(t, srv, "own", "todo.mirror", map[string]any{"session": "own", "items": []map[string]any{
		{"text": "Design approved", "status": "completed"},
		{"text": "Write the parser", "status": "in_progress"},
	}})
	if resp.Error != nil {
		t.Fatalf("mirror: %+v", resp.Error)
	}
	list, _ := srv.todos.Get("own")
	if !todo.GateOpen(list, todo.DesignGateRef) {
		t.Fatalf("mirror closed the gate: %+v", list.Items)
	}
	found := false
	for _, it := range list.Items {
		found = found || it.Text == "Write the parser"
	}
	if !found {
		t.Fatalf("mirror dropped the other items: %+v", list.Items)
	}
}

func TestEmptyCallerFromHolderTreeIsSession(t *testing.T) {
	srv := gateServer(t)
	srv.procInSession = func(pid int) bool { return pid == 4242 }
	id := gateID(t, srv)
	call := func(pid int) Response {
		b, _ := json.Marshal(map[string]any{"session": "own", "id": id, "state": "done"})
		line, _ := json.Marshal(Request{ID: "1", Method: "todo.update", Params: b})
		return srv.dispatch(withPeerPID(context.Background(), pid, true), line)
	}
	wantCode(t, call(4242), CodeGateNeedsOperator)
	if resp := call(77); resp.Error != nil {
		t.Fatalf("unrelated process refused: %+v", resp.Error)
	}
}

func TestDescendsFrom(t *testing.T) {
	parents := map[int]int{30: 20, 20: 10, 10: 1, 50: 40}
	parent := func(p int) (int, bool) { pp, ok := parents[p]; return pp, ok }
	roots := map[int]bool{10: true}
	if !descendsFrom(30, roots, parent) || !descendsFrom(10, roots, parent) {
		t.Fatal("descendant not found")
	}
	if descendsFrom(50, roots, parent) || descendsFrom(99, roots, parent) {
		t.Fatal("unrelated process matched")
	}
}

func reviewGateServer(t *testing.T) *Server {
	t.Helper()
	srv, _, _ := todoServer(t, todoSession("own"), todoSession("other"))
	rpcOK(t, srv, "", "todo.gate", map[string]any{"session": "own", "ref": "gate:review"}, nil)
	return srv
}

func TestApproveReview(t *testing.T) {
	srv := reviewGateServer(t)
	wantCode(t, rpc(t, srv, "other", "todo.approve_review", map[string]any{"session": "own", "quote": "lgtm"}), CodeGateNeedsOperator)
	wantCode(t, rpc(t, srv, "own", "todo.approve_review", map[string]any{"session": "own", "quote": " "}), CodeInvalidParams)
	rpcOK(t, srv, "own", "todo.approve_review", map[string]any{"session": "own", "quote": "lgtm"}, nil)
	list, _ := srv.todos.Get("own")
	for _, it := range list.Items {
		if it.Ref == "gate:review" && it.State != domain.TodoDone {
			t.Fatalf("gate state %s", it.State)
		}
	}
	rows, err := srv.repo.(sessionEventLister).ListSessionEvents(context.Background(), "own", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range rows {
		if r.Kind == domain.EventReviewApproved {
			n++
			if string(r.Payload) != `{"quote":"lgtm"}` {
				t.Fatalf("payload %s", r.Payload)
			}
		}
		if r.Kind == domain.EventDesignApproved {
			t.Fatal("a review approval must not record design.approved")
		}
	}
	if n != 1 {
		t.Fatalf("review.approved rows = %d", n)
	}
}

func TestReviewGateRefusesSessionCallers(t *testing.T) {
	srv := reviewGateServer(t)
	list, _ := srv.todos.Get("own")
	id := ""
	for _, it := range list.Items {
		if it.Ref == "gate:review" {
			id = it.ID
		}
	}
	wantCode(t, rpc(t, srv, "own", "todo.update", map[string]any{"session": "own", "id": id, "state": "done"}), CodeGateNeedsOperator)
}

func TestReviewGateDoesNotBlockFor(t *testing.T) {
	srv, _ := forServer(t, "")
	err := srv.TodoAccess().Mutate(context.Background(), "p", domain.AuthorAiman, func(l *domain.TodoList) error {
		todo.AddGate(l, todo.ReviewGateRef)
		for i := range l.Items {
			if l.Items[i].Ref == todo.ReviewGateRef {
				l.Items[i].ID = "g2"
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp := rpc(t, srv, "", "session.create", map[string]any{"for": "t3", "parent": "p"}); resp.Error != nil {
		t.Fatalf("create with an open review gate: %+v", resp.Error)
	}
}
