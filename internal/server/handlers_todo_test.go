package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/todo"
)

// todoServer is a serve instance with to-dos enabled over a real repo holding
// sessions, a fake steer store and a fake target that is never ready.
func todoServer(t *testing.T, sessions ...domain.Session) (*Server, *fakeSteerTarget, *fakeSteerStore) {
	t.Helper()
	old := steerCoalesceWindow
	steerCoalesceWindow = testSteerWindow
	t.Cleanup(func() { steerCoalesceWindow = old })

	repo := testRepo(t)
	for i := range sessions {
		if sessions[i].Status == "" {
			sessions[i].Status = domain.SessionStatusActive
		}
		if sessions[i].Name == "" {
			sessions[i].Name = sessions[i].ID
		}
		if sessions[i].Group == "" {
			sessions[i].Group = "g"
		}
		if err := repo.Save(context.Background(), &sessions[i]); err != nil {
			t.Fatal(err)
		}
	}
	srv := New(nil, repo, nil, nil, nil, nil, "test")
	target := &fakeSteerTarget{held: steerHeldWorking}
	srv.target = target
	notes := &fakeSteerStore{}
	srv.EnableTodos(todo.NewFiles(t.TempDir()), notes)
	return srv, target, notes
}

func todoSession(id string) domain.Session {
	return domain.Session{ID: id, TmuxSession: id, Backend: domain.BackendTmux}
}

// rpc dispatches one request as caller and returns the response.
func rpc(t *testing.T, srv *Server, caller, method string, params map[string]any) Response {
	t.Helper()
	b, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(Request{ID: "1", Method: method, Params: b, Caller: caller})
	if err != nil {
		t.Fatal(err)
	}
	return srv.dispatch(context.Background(), line)
}

// rpcOK dispatches and decodes the result into out, failing on an error.
func rpcOK(t *testing.T, srv *Server, caller, method string, params map[string]any, out any) {
	t.Helper()
	resp := rpc(t, srv, caller, method, params)
	if resp.Error != nil {
		t.Fatalf("%s: %v", method, resp.Error)
	}
	b, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatal(err)
		}
	}
}

type todoListResult struct {
	Type  string          `json:"type"`
	List  domain.TodoList `json:"list"`
	ID    string          `json:"id"`
	Steer SteerStatus     `json:"steer"`
}

type steerResult struct {
	Type     string      `json:"type"`
	Steer    SteerStatus `json:"steer"`
	Answered *bool       `json:"answered"`
}

func waitServerQueued(t *testing.T, srv *Server) {
	t.Helper()
	const id = "s1"
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.Steerer().Status(id).Queued == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("queued = %d, want 1", srv.Steerer().Status(id).Queued)
}

// settle waits past the coalescing window so anything buffered is flushed.
func settle() { time.Sleep(10 * testSteerWindow) }

func TestTodoAddFromUserSteers(t *testing.T) {
	srv, _, _ := todoServer(t, todoSession("s1"))
	var res todoListResult
	rpcOK(t, srv, "", "todo.add", map[string]any{"session": "s1", "text": "write tests"}, &res)
	if res.Type != "todo_list" || res.ID != "t1" || len(res.List.Items) != 1 || res.List.Items[0].ID != "t1" {
		t.Fatalf("result = %+v", res)
	}
	if res.List.Items[0].UpdatedBy != domain.AuthorUser {
		t.Fatalf("UpdatedBy = %q, want user", res.List.Items[0].UpdatedBy)
	}
	waitServerQueued(t, srv)
}

func TestTodoAddCollapsesWhitespace(t *testing.T) {
	srv, _, _ := todoServer(t, todoSession("s1"))
	var res todoListResult
	rpcOK(t, srv, "", "todo.add", map[string]any{"session": "s1", "text": "  write\n\n tests\tnow "}, &res)
	if got := res.List.Items[0].Text; got != "write tests now" {
		t.Fatalf("text = %q", got)
	}
	resp := rpc(t, srv, "", "todo.add", map[string]any{"session": "s1", "text": " \n\t "})
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("blank text: %+v", resp.Error)
	}
}

func TestTodoAddFromOwnAgentDoesNotSteer(t *testing.T) {
	srv, _, _ := todoServer(t, todoSession("s1"))
	var res todoListResult
	rpcOK(t, srv, "s1", "todo.add", map[string]any{"session": "s1", "text": "x", "as": "user"}, &res)
	if res.List.Items[0].UpdatedBy != domain.AuthorAgent {
		t.Fatalf("UpdatedBy = %q, want agent", res.List.Items[0].UpdatedBy)
	}
	settle()
	if q := srv.Steerer().Status("s1").Queued; q != 0 {
		t.Fatalf("queued = %d, want 0", q)
	}
}

func TestTodoAddAfterAndRef(t *testing.T) {
	srv, _, _ := todoServer(t, todoSession("s1"))
	rpcOK(t, srv, "", "todo.add", map[string]any{"session": "s1", "text": "a"}, nil)
	rpcOK(t, srv, "", "todo.add", map[string]any{"session": "s1", "text": "c"}, nil)
	var res todoListResult
	rpcOK(t, srv, "", "todo.add", map[string]any{"session": "s1", "text": "b", "after": "t1", "ref": "check:lint"}, &res)
	if res.ID != "t3" || res.List.Items[1].ID != "t3" || res.List.Items[1].Ref != "check:lint" {
		t.Fatalf("list = %+v", res.List.Items)
	}
	resp := rpc(t, srv, "", "todo.add", map[string]any{"session": "s1", "text": "d", "after": "t9"})
	if resp.Error == nil || resp.Error.Code != CodeNotFound {
		t.Fatalf("unknown after: %+v", resp.Error)
	}
}

func TestTodoUpdateMoveRemove(t *testing.T) {
	srv, _, _ := todoServer(t, todoSession("s1"))
	for _, txt := range []string{"a", "b", "c"} {
		rpcOK(t, srv, "", "todo.add", map[string]any{"session": "s1", "text": txt}, nil)
	}
	var res todoListResult
	rpcOK(t, srv, "", "todo.update", map[string]any{"session": "s1", "id": "t2", "state": "active", "text": "bee"}, &res)
	if it := res.List.Items[1]; it.State != domain.TodoActive || it.Text != "bee" {
		t.Fatalf("updated = %+v", it)
	}
	resp := rpc(t, srv, "", "todo.update", map[string]any{"session": "s1", "id": "t2", "state": "bogus"})
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("bad state: %+v", resp.Error)
	}
	rpcOK(t, srv, "", "todo.move", map[string]any{"session": "s1", "id": "t1", "to_index": 99}, &res)
	if ids := itemIDs(res.List); ids != "t2,t3,t1" {
		t.Fatalf("after move = %s", ids)
	}
	rpcOK(t, srv, "", "todo.move", map[string]any{"session": "s1", "id": "t1", "to_index": -4}, &res)
	if ids := itemIDs(res.List); ids != "t1,t2,t3" {
		t.Fatalf("after clamp to 0 = %s", ids)
	}
	rpcOK(t, srv, "", "todo.remove", map[string]any{"session": "s1", "id": "t3"}, &res)
	if ids := itemIDs(res.List); ids != "t1,t2" {
		t.Fatalf("after remove = %s", ids)
	}
}

func itemIDs(l domain.TodoList) string {
	ids := make([]string, 0, len(l.Items))
	for _, it := range l.Items {
		ids = append(ids, it.ID)
	}
	return strings.Join(ids, ",")
}

func TestTodoMirrorIsAlwaysAgent(t *testing.T) {
	srv, _, notes := todoServer(t, todoSession("s1"))
	var res todoListResult
	rpcOK(t, srv, "", "todo.mirror", map[string]any{"session": "s1", "as": "user", "items": []map[string]any{
		{"text": "plan", "status": "completed"},
		{"text": "build", "status": "in_progress"},
	}}, &res)
	if len(res.List.Items) != 2 {
		t.Fatalf("items = %+v", res.List.Items)
	}
	for _, it := range res.List.Items {
		if it.UpdatedBy != domain.AuthorAgent {
			t.Fatalf("item %s UpdatedBy = %q, want agent", it.ID, it.UpdatedBy)
		}
	}
	if res.List.Items[0].State != domain.TodoDone || res.List.Items[1].State != domain.TodoActive {
		t.Fatalf("states = %+v", res.List.Items)
	}
	settle()
	if q := srv.Steerer().Status("s1").Queued; q != 0 || len(notes.records()) != 0 {
		t.Fatalf("queued = %d, records = %d; want none", q, len(notes.records()))
	}
}

func TestTodoMutationRendersFile(t *testing.T) {
	sess := todoSession("s1")
	sess.WorkingDirectory = t.TempDir()
	srv, _, _ := todoServer(t, sess)
	rpcOK(t, srv, "", "todo.add", map[string]any{"session": "s1", "text": "render me"}, nil)
	b, err := os.ReadFile(filepath.Join(sess.WorkingDirectory, domain.AimanTodoFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "t1") {
		t.Fatalf("rendered file lacks t1:\n%s", b)
	}
}

func TestTodoMutationPublishesTodoChanged(t *testing.T) {
	srv, _, _ := todoServer(t, todoSession("s1"))
	ch, cancel := srv.hub.Subscribe()
	defer cancel()
	rpcOK(t, srv, "", "todo.add", map[string]any{"session": "s1", "text": "x"}, nil)
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == "todo_changed" {
				if ev.ID != "s1" || ev.Rev != 1 {
					t.Fatalf("event = %+v", ev)
				}
				return
			}
		case <-deadline:
			t.Fatal("no todo_changed event")
		}
	}
}

func TestTodoUnknownItemIsNotFound(t *testing.T) {
	srv, _, _ := todoServer(t, todoSession("s1"))
	for _, tc := range []struct {
		method string
		params map[string]any
	}{
		{"todo.update", map[string]any{"session": "s1", "id": "t9", "state": "done"}},
		{"todo.move", map[string]any{"session": "s1", "id": "t9", "to_index": 0}},
		{"todo.remove", map[string]any{"session": "s1", "id": "t9"}},
		{"todo.get", map[string]any{"session": "nope"}},
	} {
		resp := rpc(t, srv, "", tc.method, tc.params)
		if resp.Error == nil || resp.Error.Code != CodeNotFound {
			t.Fatalf("%s: error = %+v, want not_found", tc.method, resp.Error)
		}
	}
}

func TestTodoGetIncludesSteer(t *testing.T) {
	srv, _, _ := todoServer(t, todoSession("s1"))
	rpcOK(t, srv, "", "steer.send", map[string]any{"session": "s1", "text": "hi"}, nil)
	waitServerQueued(t, srv)
	var res todoListResult
	rpcOK(t, srv, "", "todo.get", map[string]any{"session": "s1"}, &res)
	if res.Type != "todo_list" || res.List.SessionID != "s1" || res.Steer.Queued != 1 {
		t.Fatalf("result = %+v", res)
	}
}

func TestTodosDisabled(t *testing.T) {
	srv := New(nil, testRepo(t), nil, nil, nil, nil, "test")
	for _, m := range []string{"todo.get", "todo.add", "todo.update", "todo.move", "todo.remove", "todo.mirror", "todo.summary", "steer.send"} {
		resp := rpc(t, srv, "", m, map[string]any{"session": "s1", "text": "x"})
		if resp.Error == nil || resp.Error.Code != CodeInvalidParams || resp.Error.Message != "to-dos not enabled" {
			t.Fatalf("%s: %+v", m, resp.Error)
		}
	}
	st := srv.Steerer()
	if st == nil {
		t.Fatal("Steerer() returned nil")
	}
	st.Enqueue(SteerNote{SessionID: "s1", Author: domain.AuthorUser, Text: "x"})
	if q := st.Status("s1").Queued; q != 0 {
		t.Fatalf("no-op steerer queued %d", q)
	}
}

func TestDeriveAuthor(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		method, caller, tgt, as string
		want                    domain.TodoAuthor
		wantErr                 bool
	}{
		{"mirror as user", "todo.mirror", "", "s1", "user", domain.AuthorAgent, false},
		{"mirror as aiman", "todo.mirror", "other", "s1", "aiman", domain.AuthorAgent, false},
		{"own agent", "todo.add", "s1", "s1", "user", domain.AuthorAgent, false},
		{"empty as", "todo.add", "", "s1", "", domain.AuthorUser, false},
		{"other session", "todo.add", "s2", "s1", "", domain.AuthorUser, false},
		{"user", "todo.update", "", "s1", "user", domain.AuthorUser, false},
		{"aiman on todo", "todo.add", "", "s1", "aiman", "", true},
		{"verifier", "todo.add", "", "s1", "verifier", "", true},
		{"agent claimed", "todo.add", "", "s1", "agent", "", true},
		{"steer as aiman", "steer.send", "", "s1", "aiman", domain.AuthorAiman, false},
		{"steer self", "steer.send", "s1", "s1", "", "", true},
		{"steer user", "steer.send", "s2", "s1", "user", domain.AuthorUser, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := deriveAuthor(tc.method, tc.caller, tc.tgt, tc.as)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("deriveAuthor = (%q, %v), want (%q, err=%v)", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestSteerSendQueuesWithoutWrapper(t *testing.T) {
	srv, _, notes := todoServer(t, todoSession("s1"))
	var res steerResult
	rpcOK(t, srv, "", "steer.send", map[string]any{"session": "s1", "text": "please\n  rebase"}, &res)
	if res.Type != "steer_status" || res.Answered != nil {
		t.Fatalf("result = %+v", res)
	}
	waitServerQueued(t, srv)
	recs := notes.records()
	if len(recs) != 1 || recs[0].Text != "please rebase" || recs[0].Author != domain.AuthorUser {
		t.Fatalf("records = %+v", recs)
	}
}

func TestSteerSendRejectsSelf(t *testing.T) {
	srv, _, notes := todoServer(t, todoSession("s1"))
	resp := rpc(t, srv, "s1", "steer.send", map[string]any{"session": "s1", "text": "hello me"})
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams || !strings.Contains(resp.Error.Message, "cannot steer itself") {
		t.Fatalf("error = %+v", resp.Error)
	}
	settle()
	if q := srv.Steerer().Status("s1").Queued; q != 0 || len(notes.records()) != 0 {
		t.Fatalf("queued %d, records %d", q, len(notes.records()))
	}
}

func TestSteerSendAnswerTypesOnDialog(t *testing.T) {
	srv, target, notes := todoServer(t, todoSession("s1"))
	target.setReady(false, steerHeldDialog)
	var res steerResult
	rpcOK(t, srv, "", "steer.send", map[string]any{"session": "s1", "text": "1", "mode": "answer", "as": "aiman"}, &res)
	if res.Answered == nil || !*res.Answered {
		t.Fatalf("answered = %v, want true", res.Answered)
	}
	if got := target.deliveries(); len(got) != 1 || got[0] != "1" {
		t.Fatalf("deliveries = %q", got)
	}
	settle()
	if len(notes.records()) != 0 || srv.Steerer().Status("s1").Queued != 0 {
		t.Fatal("answer must not be queued or persisted")
	}
}

func TestSteerSendAnswerSkipsWhenNotDialog(t *testing.T) {
	srv, target, notes := todoServer(t, todoSession("s1"))
	target.setReady(false, steerHeldWorking)
	var res steerResult
	rpcOK(t, srv, "", "steer.send", map[string]any{"session": "s1", "text": "1", "mode": "answer"}, &res)
	if res.Answered == nil || *res.Answered {
		t.Fatalf("answered = %v, want false", res.Answered)
	}
	settle()
	if len(target.deliveries()) != 0 || len(notes.records()) != 0 || srv.Steerer().Status("s1").Queued != 0 {
		t.Fatal("nothing should be typed or queued")
	}
}

func TestSteerSendUnknownMode(t *testing.T) {
	srv, _, _ := todoServer(t, todoSession("s1"))
	resp := rpc(t, srv, "", "steer.send", map[string]any{"session": "s1", "text": "x", "mode": "shout"})
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("error = %+v", resp.Error)
	}
}

func TestSteerSendAsAiman(t *testing.T) {
	srv, _, notes := todoServer(t, todoSession("s1"))
	rpcOK(t, srv, "", "steer.send", map[string]any{"session": "s1", "text": "ci is red", "as": "aiman"}, nil)
	waitServerQueued(t, srv)
	if recs := notes.records(); len(recs) != 1 || recs[0].Author != domain.AuthorAiman {
		t.Fatalf("records = %+v", recs)
	}
	resp := rpc(t, srv, "", "todo.add", map[string]any{"session": "s1", "text": "x", "as": "aiman"})
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("todo.add as aiman: %+v", resp.Error)
	}
}

func TestTodoSummary(t *testing.T) {
	srv, _, _ := todoServer(t, todoSession("a"), todoSession("b"), todoSession("c"))
	ctx := context.Background()
	add := func(id string, items ...domain.TodoItem) {
		if _, err := srv.MutateTodos(ctx, mustSession(t, srv, id), domain.AuthorUser, func(l *domain.TodoList) error {
			l.Items = append(l.Items, items...)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	add("a",
		domain.TodoItem{Text: "one", State: domain.TodoDone},
		domain.TodoItem{Text: "two", State: domain.TodoActive},
		domain.TodoItem{Text: "gone", State: domain.TodoDropped},
	)
	add("b", domain.TodoItem{Text: "only", State: domain.TodoPending})

	type entry struct {
		Done   int    `json:"done"`
		Total  int    `json:"total"`
		Active string `json:"active"`
		Rev    int64  `json:"rev"`
	}
	var res struct {
		Type     string           `json:"type"`
		Sessions map[string]entry `json:"sessions"`
	}
	rpcOK(t, srv, "", "todo.summary", map[string]any{}, &res)
	if res.Type != "todo_summary" || len(res.Sessions) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if got := res.Sessions["a"]; got != (entry{Done: 1, Total: 2, Active: "two", Rev: 1}) {
		t.Fatalf("a = %+v", got)
	}
	if got := res.Sessions["b"]; got != (entry{Done: 0, Total: 1, Rev: 1}) {
		t.Fatalf("b = %+v", got)
	}
	res.Sessions = nil
	rpcOK(t, srv, "", "todo.summary", map[string]any{"sessions": []string{"a"}}, &res)
	if len(res.Sessions) != 1 || res.Sessions["a"].Total != 2 {
		t.Fatalf("filtered = %+v", res.Sessions)
	}
}

func mustSession(t *testing.T, srv *Server, id string) domain.Session {
	t.Helper()
	sess, ok := srv.steerSession(context.Background(), id)
	if !ok {
		t.Fatalf("session %s not found", id)
	}
	return sess
}

func TestTodoAccessMutateUsesServePath(t *testing.T) {
	srv, _, notes := todoServer(t, todoSession("s1"))
	ch, cancel := srv.hub.Subscribe()
	defer cancel()
	acc := srv.TodoAccess()
	err := acc.Mutate(context.Background(), "s1", domain.AuthorAiman, func(l *domain.TodoList) error {
		l.Items = append(l.Items, domain.TodoItem{Text: "fix ci", State: domain.TodoPending})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := acc.Get("s1")
	if err != nil || len(got.Items) != 1 || got.Items[0].UpdatedBy != domain.AuthorAiman {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	select {
	case ev := <-ch:
		if ev.Type != "todo_changed" || ev.ID != "s1" {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no todo_changed")
	}
	waitServerQueued(t, srv)
	if recs := notes.records(); len(recs) != 1 || recs[0].Author != domain.AuthorAiman {
		t.Fatalf("records = %+v", recs)
	}
	if err := acc.Mutate(context.Background(), "missing", domain.AuthorAiman, func(*domain.TodoList) error { return nil }); err == nil {
		t.Fatal("Mutate on unknown session should fail")
	}
}
