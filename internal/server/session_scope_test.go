package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/ptyruntime/ptyfake"
	"github.com/bouwerp/aiman/internal/todo"
)

// The scope family: sess-a has a child sess-c, which has a child sess-d.
// sess-b is unrelated. sess-a-run and sess-b-run are helper ptys with no row,
// named the way the bundled skill names its runner ("${AIMAN_ID}-run").
const (
	scopeA     = "sess-a"
	scopeB     = "sess-b"
	scopeC     = "sess-c"
	scopeD     = "sess-d"
	scopeARun  = "sess-a-run"
	scopeBRun  = "sess-b-run"
	scopePeer  = 4242
	scopeNoPID = 0
)

func scopeRow(id, parent string) domain.Session {
	return domain.Session{
		ID: id, Name: id, Group: "g", TerminalName: id, ParentID: parent,
		Backend: domain.BackendPTY, WorkingDirectory: "/w/" + id,
	}
}

// scopeServer is a serve instance over a fake runtime holding the scope family,
// with to-dos and steering enabled.
func scopeServer(t *testing.T) (*Server, *ptyfake.FakePTY) {
	t.Helper()
	old := steerCoalesceWindow
	steerCoalesceWindow = testSteerWindow
	t.Cleanup(func() { steerCoalesceWindow = old })
	srv, pty, _ := ptyServer(t,
		scopeRow(scopeA, ""), scopeRow(scopeB, ""),
		scopeRow(scopeC, scopeA), scopeRow(scopeD, scopeC))
	for _, id := range []string{scopeA, scopeB, scopeC, scopeD, scopeARun, scopeBRun} {
		pty.Add(ptyruntime.SessionInfo{ID: id, Name: id, Dir: "/w/" + id})
	}
	srv.procInSession = func(pid int) bool { return pid == scopePeer }
	srv.target = &fakeSteerTarget{held: steerHeldWorking}
	srv.EnableTodos(todo.NewFiles(t.TempDir()), &fakeSteerStore{})
	return srv, pty
}

// scopeCall dispatches one request as caller, from peer pid when it is set.
func scopeCall(t *testing.T, srv *Server, caller string, pid int, method string, params map[string]any) Response {
	t.Helper()
	b, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(Request{ID: "1", Method: method, Params: b, Caller: caller})
	if err != nil {
		t.Fatal(err)
	}
	return srv.dispatch(withPeerPID(context.Background(), pid, pid != scopeNoPID), line)
}

type scopeCase struct {
	name    string
	caller  string
	pid     int
	target  string
	allowed bool
}

// scopeTargetCases are the callers and targets every session-targeting method
// is checked with. ptyOnly cases target a helper pty that has no session row.
func scopeTargetCases(ptyOnly bool) []scopeCase {
	cases := []scopeCase{
		{name: "self", caller: scopeA, target: scopeA, allowed: true},
		{name: "child", caller: scopeA, target: scopeC, allowed: true},
		{name: "grandchild", caller: scopeA, target: scopeD, allowed: true},
		{name: "unrelated", caller: scopeA, target: scopeB},
		{name: "child to parent", caller: scopeC, target: scopeA},
		{name: "holder peer without caller", pid: scopePeer, target: scopeB},
		{name: "operator", target: scopeB, allowed: true},
		{name: "operator peer outside holders", pid: 77, target: scopeB, allowed: true},
	}
	if ptyOnly {
		cases = append(cases,
			scopeCase{name: "own helper", caller: scopeA, target: scopeARun, allowed: true},
			scopeCase{name: "other helper", caller: scopeA, target: scopeBRun})
	}
	return cases
}

// TestSessionCallerScopedToOwnTree checks every method that acts on another
// session: a session caller may reach only itself and its descendants, and an
// operator is unchanged.
func TestSessionCallerScopedToOwnTree(t *testing.T) {
	methods := []struct {
		method  string
		ptyOnly bool
		params  func(target string) map[string]any
	}{
		{"pty.input", true, func(id string) map[string]any { return map[string]any{"id": id, "data": "x"} }},
		{"pty.run", true, func(id string) map[string]any { return map[string]any{"id": id, "command": "ls"} }},
		{"pty.kill", true, func(id string) map[string]any { return map[string]any{"id": id} }},
		{"pty.forget", true, func(id string) map[string]any { return map[string]any{"id": id} }},
		{"session.prompt", false, func(id string) map[string]any { return map[string]any{"id": id, "text": "hi"} }},
		{"session.forget", false, func(id string) map[string]any { return map[string]any{"id": id} }},
		{"steer.send", false, func(id string) map[string]any {
			return map[string]any{"session": id, "text": "hi", "mode": "answer"}
		}},
	}
	for _, m := range methods {
		for _, c := range scopeTargetCases(m.ptyOnly) {
			t.Run(m.method+"/"+c.name, func(t *testing.T) {
				srv, pty := scopeServer(t)
				resp := scopeCall(t, srv, c.caller, c.pid, m.method, m.params(c.target))
				checkScope(t, srv, pty, resp, c.allowed)
			})
		}
	}
}

// checkScope asserts an allowed call was not refused for scope (it may fail
// for another reason), and a refused one is session_scope with no effect.
func checkScope(t *testing.T, srv *Server, pty *ptyfake.FakePTY, resp Response, allowed bool) {
	t.Helper()
	if allowed {
		if resp.Error != nil && resp.Error.Code == CodeSessionScope {
			t.Fatalf("allowed call refused: %+v", resp.Error)
		}
		return
	}
	if resp.Error == nil || resp.Error.Code != CodeSessionScope {
		t.Fatalf("error = %+v, want %s", resp.Error, CodeSessionScope)
	}
	if n := len(pty.Writes()) + len(pty.Kills()) + len(pty.Forgets()) + len(pty.Creates()); n != 0 {
		t.Fatalf("refused call reached the runtime: writes %v kills %v forgets %v creates %v",
			pty.Writes(), pty.Kills(), pty.Forgets(), pty.Creates())
	}
	if _, ok := srv.findSession(context.Background(), scopeB); !ok {
		t.Fatal("refused call dropped sess-b's row")
	}
}

// TestSessionCallerPTYCreateScoped: a session may create a pty only under
// itself, or a helper whose id starts with its own id and that names no other
// parent.
func TestSessionCallerPTYCreateScoped(t *testing.T) {
	cases := []struct {
		name    string
		caller  string
		pid     int
		id      string
		parent  string
		allowed bool
	}{
		{name: "child of caller", caller: scopeA, id: "new-1", parent: scopeA, allowed: true},
		{name: "own helper", caller: scopeA, id: scopeA + "-run2", allowed: true},
		{name: "child of unrelated", caller: scopeA, id: "new-2", parent: scopeB},
		{name: "child of parent", caller: scopeC, id: "new-3", parent: scopeA},
		{name: "own-prefixed id under another parent", caller: scopeA, id: scopeA + "-run3", parent: scopeB},
		{name: "other session's helper", caller: scopeA, id: scopeB + "-run2"},
		{name: "loose pty", caller: scopeA, id: "loose"},
		{name: "holder peer without caller", pid: scopePeer, id: "new-4", parent: scopeB},
		{name: "operator", id: "new-5", parent: scopeB, allowed: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, pty := scopeServer(t)
			params := map[string]any{"id": c.id, "command": "sh"}
			if c.parent != "" {
				params["parent_id"] = c.parent
			}
			resp := scopeCall(t, srv, c.caller, c.pid, "pty.create", params)
			checkScope(t, srv, pty, resp, c.allowed)
		})
	}
}
