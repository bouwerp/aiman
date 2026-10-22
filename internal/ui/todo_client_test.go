package ui

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/usecase"
)

// forbidExec satisfies the RPC executor. Execute fails the test path instead
// of opening a socket; the recording rpc must not call it.
type forbidExec struct{}

func (forbidExec) Execute(context.Context, string) (string, error) {
	return "", errors.New("ssh execute called")
}

func (forbidExec) WriteFile(context.Context, string, []byte) error {
	return errors.New("ssh write called")
}

type rpcCapture struct {
	method string
	params any
}

func testRemote() config.Remote {
	return config.Remote{Host: "regent0", User: "me", Root: "/work"}
}

func recordingClient(t *testing.T, body []byte, rpcErr error) (*sshTodoClient, *[]rpcCapture) {
	t.Helper()
	var calls []rpcCapture
	c := newSSHTodoClient()
	c.dial = func(r config.Remote) usecase.TerminalExecutor {
		if r.Host != "regent0" || r.User != "me" || r.Root != "/work" {
			t.Errorf("dial remote: %+v", r)
		}
		return forbidExec{}
	}
	c.rpc = func(_ context.Context, remote usecase.TerminalExecutor, method string, params any) (json.RawMessage, error) {
		if _, ok := remote.(forbidExec); !ok {
			t.Errorf("rpc executor %T, want forbidExec", remote)
		}
		calls = append(calls, rpcCapture{method: method, params: params})
		return body, rpcErr
	}
	return c, &calls
}

func paramsMap(t *testing.T, params any) map[string]any {
	t.Helper()
	m, ok := params.(map[string]any)
	if !ok {
		t.Fatalf("params type %T, want map[string]any", params)
	}
	return m
}

func TestSSHTodoClientAddsUserAuthor(t *testing.T) {
	c, calls := recordingClient(t, []byte(`{"type":"steer_status","steer":{"queued":1}}`), nil)
	ctx := context.Background()
	remote := testRemote()

	ops := []todoOp{
		opAdd("s1", "buy milk", "t0"),
		opUpdateText("s1", "t1", "renamed"),
		opSetState("s1", "t1", domain.TodoActive),
		opMove("s1", "t1", 3),
		opRemove("s1", "t1"),
	}
	for _, op := range ops {
		if _, has := op.Params["as"]; has {
			t.Fatalf("%s params include as before Apply", op.Method)
		}
		if err := c.Apply(ctx, remote, op); err != nil {
			t.Fatal(err)
		}
	}
	st, err := c.Prompt(ctx, remote, "s1", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if st.Queued != 1 {
		t.Fatalf("steer queued %d", st.Queued)
	}
	if err := c.Apply(ctx, remote, opAdd("s1", "no after", "")); err != nil {
		t.Fatal(err)
	}

	if len(*calls) != 7 {
		t.Fatalf("calls: %d", len(*calls))
	}
	wantMethods := []string{"todo.add", "todo.update", "todo.update", "todo.move", "todo.remove", "steer.send", "todo.add"}
	for i, call := range *calls {
		m := paramsMap(t, call.params)
		if call.method != wantMethods[i] {
			t.Errorf("call %d method %s, want %s", i, call.method, wantMethods[i])
		}
		if m["as"] != "user" {
			t.Errorf("call %d %s as=%#v", i, call.method, m["as"])
		}
	}

	add := paramsMap(t, (*calls)[0].params)
	if add["session"] != "s1" || add["text"] != "buy milk" || add["after"] != "t0" {
		t.Fatalf("add params: %#v", add)
	}
	if paramsMap(t, (*calls)[1].params)["text"] != "renamed" {
		t.Fatalf("update text: %#v", (*calls)[1].params)
	}
	state := paramsMap(t, (*calls)[2].params)["state"]
	if state != "active" && state != domain.TodoActive {
		t.Fatalf("state %#v", state)
	}
	if paramsMap(t, (*calls)[3].params)["to_index"] != 3 {
		t.Fatalf("move: %#v", (*calls)[3].params)
	}
	if paramsMap(t, (*calls)[4].params)["id"] != "t1" {
		t.Fatalf("remove: %#v", (*calls)[4].params)
	}

	prompt := paramsMap(t, (*calls)[5].params)
	if prompt["mode"] != "note" || prompt["session"] != "s1" || prompt["text"] != "hello" {
		t.Fatalf("prompt: %#v", prompt)
	}
	noAfter := paramsMap(t, (*calls)[6].params)
	if _, ok := noAfter["after"]; ok {
		t.Fatalf("empty after was sent: %#v", noAfter)
	}
}

func TestSSHTodoClientGetDecodes(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"type": "todo_list",
		"list": domain.TodoList{
			SessionID:  "s1",
			Rev:        4,
			Items:      []domain.TodoItem{{ID: "t1", State: domain.TodoActive}},
			NotInAgent: []string{"t1"},
		},
		"steer": server.SteerStatus{Queued: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	c, calls := recordingClient(t, body, nil)
	snap, err := c.Get(context.Background(), testRemote(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.List.Rev != 4 || snap.Steer.Queued != 2 || !snap.HasList {
		t.Fatalf("%+v", snap)
	}
	if len(snap.List.NotInAgent) != 1 || snap.List.NotInAgent[0] != "t1" {
		t.Fatalf("not in agent: %#v", snap.List.NotInAgent)
	}
	if len(snap.List.Items) != 1 || snap.List.Items[0].ID != "t1" || snap.List.Items[0].State != domain.TodoActive {
		t.Fatalf("items: %#v", snap.List.Items)
	}
	m := paramsMap(t, (*calls)[0].params)
	if (*calls)[0].method != "todo.get" || m["session"] != "s1" {
		t.Fatalf("get call: %#v", (*calls)[0])
	}
	if _, ok := m["as"]; ok {
		t.Fatal("todo.get sent as")
	}
}

func TestSSHTodoClientSummaryDecodes(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"type": "todo_summary",
		"sessions": map[string]any{
			"a": map[string]any{"done": 3, "total": 7, "active": "Run preprod", "rev": 9},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	c, calls := recordingClient(t, body, nil)
	ctx := context.Background()
	remote := testRemote()

	got, err := c.Summary(ctx, remote, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if got["a"] != (todoSummary{Done: 3, Total: 7, Active: "Run preprod", Rev: 9}) {
		t.Fatalf("%+v", got["a"])
	}
	m := paramsMap(t, (*calls)[0].params)
	sessions, ok := m["sessions"].([]string)
	if !ok || len(sessions) != 1 || sessions[0] != "a" {
		t.Fatalf("sessions: %#v", m["sessions"])
	}
	if _, ok := m["as"]; ok {
		t.Fatal("todo.summary sent as")
	}

	if _, err := c.Summary(ctx, remote, nil); err != nil {
		t.Fatal(err)
	}
	nilParams := paramsMap(t, (*calls)[1].params)
	if _, ok := nilParams["sessions"]; ok {
		t.Fatalf("nil ids sent sessions: %#v", nilParams)
	}
	if _, ok := nilParams["as"]; ok {
		t.Fatal("nil summary sent as")
	}
}

func TestNewSSHTodoClientDialsSSHConfig(t *testing.T) {
	c := newSSHTodoClient()
	if reflect.ValueOf(c.rpc).Pointer() != reflect.ValueOf(rpcFunc(usecase.ServeRPC)).Pointer() {
		t.Fatal("rpc is not usecase.ServeRPC")
	}
	ex := c.dial(config.Remote{Host: "h.example", User: "alice", Root: "/srv"})
	mgr, ok := ex.(*ssh.Manager)
	if !ok {
		t.Fatalf("%T", ex)
	}
	cfg := reflect.ValueOf(mgr).Elem().FieldByName("config")
	if cfg.FieldByName("Host").String() != "h.example" ||
		cfg.FieldByName("User").String() != "alice" ||
		cfg.FieldByName("Root").String() != "/srv" {
		t.Fatal("ssh.Config was not filled from config.Remote")
	}
}

// fakePrompt is one Prompt call recorded by fakeTodoClient.
type fakePrompt struct {
	Session string
	Text    string
}

// fakeTodoClient is the in-process todoClient later dashboard tests drive.
// gets, ops, prompts, and summaries record the calls. snap and summary are
// the default answers. snaps and errs override them for one session id.
// Summary(nil) ignores errs and returns a copy of summary.
type fakeTodoClient struct {
	gets      []string
	ops       []todoOp
	prompts   []fakePrompt
	summaries [][]string

	snap    todoSnapshot
	snaps   map[string]todoSnapshot
	summary map[string]todoSummary
	err     error
	errs    map[string]error
}

var _ todoClient = (*fakeTodoClient)(nil)

func (f *fakeTodoClient) errFor(id string) error {
	if f.errs != nil {
		if err, ok := f.errs[id]; ok {
			return err
		}
	}
	return f.err
}

func (f *fakeTodoClient) snapshot(id string) todoSnapshot {
	if f.snaps != nil {
		if s, ok := f.snaps[id]; ok {
			return s
		}
	}
	return f.snap
}

func (f *fakeTodoClient) Get(_ context.Context, _ config.Remote, sessionID string) (todoSnapshot, error) {
	f.gets = append(f.gets, sessionID)
	if err := f.errFor(sessionID); err != nil {
		return todoSnapshot{}, err
	}
	return f.snapshot(sessionID), nil
}

func (f *fakeTodoClient) Summary(_ context.Context, _ config.Remote, sessionIDs []string) (map[string]todoSummary, error) {
	if sessionIDs == nil {
		f.summaries = append(f.summaries, nil)
		if f.err != nil {
			return nil, f.err
		}
		return cloneSummary(f.summary), nil
	}
	f.summaries = append(f.summaries, append([]string(nil), sessionIDs...))
	for _, id := range sessionIDs {
		if err := f.errFor(id); err != nil {
			return nil, err
		}
	}
	out := map[string]todoSummary{}
	for _, id := range sessionIDs {
		if s, ok := f.summary[id]; ok {
			out[id] = s
		}
	}
	return out, nil
}

func cloneSummary(in map[string]todoSummary) map[string]todoSummary {
	out := make(map[string]todoSummary, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (f *fakeTodoClient) Apply(_ context.Context, _ config.Remote, op todoOp) error {
	f.ops = append(f.ops, op)
	id, _ := op.Params["session"].(string)
	return f.errFor(id)
}

func (f *fakeTodoClient) Prompt(_ context.Context, _ config.Remote, sessionID, text string) (server.SteerStatus, error) {
	f.prompts = append(f.prompts, fakePrompt{Session: sessionID, Text: text})
	if err := f.errFor(sessionID); err != nil {
		return server.SteerStatus{}, err
	}
	return f.snapshot(sessionID).Steer, nil
}

func TestFakeTodoClientRecordsPerSession(t *testing.T) {
	snap := newTodoSnapshot(
		domain.TodoList{SessionID: "s1", Rev: 2, Items: []domain.TodoItem{{ID: "t1"}}},
		server.SteerStatus{Queued: 4},
	)
	other := newTodoSnapshot(domain.TodoList{SessionID: "s2", Rev: 1}, server.SteerStatus{Queued: 9})
	f := &fakeTodoClient{
		snap:  snap,
		snaps: map[string]todoSnapshot{"s2": other},
		summary: map[string]todoSummary{
			"s1": {Done: 1, Total: 2, Active: "a", Rev: 2},
			"s2": {Done: 0, Total: 1, Rev: 1},
		},
		errs: map[string]error{"bad": errors.New("nope")},
	}
	ctx := context.Background()
	remote := testRemote()

	got, err := f.Get(ctx, remote, "s1")
	if err != nil || got.List.Rev != 2 || got.Steer.Queued != 4 || !got.HasList {
		t.Fatalf("default snap: %+v %v", got, err)
	}
	got, err = f.Get(ctx, remote, "s2")
	if err != nil || got.Steer.Queued != 9 {
		t.Fatalf("per-session snap: %+v %v", got, err)
	}
	if _, err := f.Get(ctx, remote, "bad"); err == nil || err.Error() != "nope" {
		t.Fatalf("per-session err: %v", err)
	}

	sum, err := f.Summary(ctx, remote, []string{"s1"})
	if err != nil || sum["s1"].Rev != 2 || len(sum) != 1 {
		t.Fatalf("summary: %+v %v", sum, err)
	}
	all, err := f.Summary(ctx, remote, nil)
	if err != nil || all["s2"].Total != 1 {
		t.Fatalf("all: %+v %v", all, err)
	}

	if err := f.Apply(ctx, remote, opAdd("s1", "x", "")); err != nil {
		t.Fatal(err)
	}
	st, err := f.Prompt(ctx, remote, "s1", "note")
	if err != nil || st.Queued != 4 {
		t.Fatalf("prompt: %+v %v", st, err)
	}
	if err := f.Apply(ctx, remote, opRemove("bad", "t1")); err == nil {
		t.Fatal("expected per-session error")
	}

	f.err = errors.New("down")
	if _, err := f.Get(ctx, remote, "s1"); err == nil || err.Error() != "down" {
		t.Fatalf("default err: %v", err)
	}

	if len(f.gets) != 4 || f.gets[0] != "s1" || f.gets[1] != "s2" || f.gets[2] != "bad" {
		t.Fatalf("gets: %#v", f.gets)
	}
	if len(f.ops) != 2 || f.ops[0].Method != "todo.add" || f.ops[0].Params["text"] != "x" {
		t.Fatalf("ops: %#v", f.ops)
	}
	if _, has := f.ops[0].Params["as"]; has {
		t.Fatal("recorded op contains as")
	}
	if len(f.prompts) != 1 || f.prompts[0] != (fakePrompt{Session: "s1", Text: "note"}) {
		t.Fatalf("prompts: %#v", f.prompts)
	}
	if len(f.summaries) != 2 || len(f.summaries[0]) != 1 || f.summaries[0][0] != "s1" || f.summaries[1] != nil {
		t.Fatalf("summaries: %#v", f.summaries)
	}
}

func TestSSHTodoClientSurfacesErrors(t *testing.T) {
	c, _ := recordingClient(t, nil, usecase.ErrRPCUnsupported)
	ctx := context.Background()
	remote := testRemote()
	cases := []struct {
		method string
		call   func() error
	}{
		{"todo.get", func() error {
			_, err := c.Get(ctx, remote, "s1")
			return err
		}},
		{"todo.summary", func() error {
			_, err := c.Summary(ctx, remote, nil)
			return err
		}},
		{"todo.add", func() error {
			return c.Apply(ctx, remote, opAdd("s9", "x", ""))
		}},
		{"steer.send", func() error {
			_, err := c.Prompt(ctx, remote, "s1", "hi")
			return err
		}},
	}
	for _, tc := range cases {
		err := tc.call()
		if !errors.Is(err, usecase.ErrRPCUnsupported) {
			t.Errorf("%s: errors.Is: %v", tc.method, err)
		}
		if err == nil || !strings.Contains(err.Error(), tc.method) {
			t.Errorf("%s: message %v", tc.method, err)
		}
	}
}
