package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/ptyruntime/ptyfake"
)

func rulesRow(id, parent string) domain.Session {
	return domain.Session{
		ID: id, Name: id, Group: "g", TerminalName: id, ParentID: parent,
		Backend: domain.BackendPTY, WorkingDirectory: "/w/" + id,
	}
}

func rulesServer(t *testing.T, rows []domain.Session, ptys ...ptyruntime.SessionInfo) (*Server, *ptyfake.FakePTY) {
	t.Helper()
	srv, pty, _ := ptyServer(t, rows...)
	for _, info := range ptys {
		pty.Add(info)
	}
	return srv, pty
}

func rulesCall(t *testing.T, srv *Server, caller, method string, params map[string]any) Response {
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

func scopeRefused(resp Response) bool {
	return resp.Error != nil && resp.Error.Code == CodeSessionScope
}

// A pty id reaches the holder through filepath.Join, so a helper-shaped id must
// not climb out to another session's directory.
func TestScopeRefusesPathInTarget(t *testing.T) {
	srv, pty := rulesServer(t, []domain.Session{rulesRow("a", ""), rulesRow("b", "")},
		ptyruntime.SessionInfo{ID: "a"}, ptyruntime.SessionInfo{ID: "b"})
	for _, id := range []string{"a-/../b", "a-x/../../pty/b", "../pty/b"} {
		if resp := rulesCall(t, srv, "a", "pty.kill", map[string]any{"id": id}); !scopeRefused(resp) {
			t.Errorf("pty.kill %q: error = %+v, want session_scope", id, resp.Error)
		}
	}
	if len(pty.Kills()) != 0 {
		t.Fatalf("kills reached the runtime: %v", pty.Kills())
	}
}

func TestScopeHelperPrefixRules(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		allowed bool
	}{
		{"own helper", "a-run", true},
		{"id that only starts with the same letters", "ab", false},
		// The dashboard registers discovered ptys, so a helper can have a row.
		{"own helper with a row", "a-registered", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := rulesServer(t, []domain.Session{rulesRow("a", ""), rulesRow("a-registered", "")},
				ptyruntime.SessionInfo{ID: "a"}, ptyruntime.SessionInfo{ID: "a-run"},
				ptyruntime.SessionInfo{ID: "ab"}, ptyruntime.SessionInfo{ID: "a-registered"})
			resp := rulesCall(t, srv, "a", "pty.input", map[string]any{"id": c.target, "data": "x"})
			if scopeRefused(resp) == c.allowed {
				t.Fatalf("error = %+v, allowed = %v", resp.Error, c.allowed)
			}
		})
	}
}

// A caller that is neither a session row nor a pty has no identity to scope by.
func TestScopeUnknownCallerReachesNothing(t *testing.T) {
	srv, pty := rulesServer(t, []domain.Session{rulesRow("sess-a", "")},
		ptyruntime.SessionInfo{ID: "sess-a"}, ptyruntime.SessionInfo{ID: "sess-x"})
	for _, target := range []string{"sess-x", "sess-a", "ghost"} {
		resp := rulesCall(t, srv, "ghost", "pty.input", map[string]any{"id": target, "data": "x"})
		if !scopeRefused(resp) {
			t.Errorf("target %q: error = %+v, want session_scope", target, resp.Error)
		}
	}
	if resp := rulesCall(t, srv, "sess", "pty.input", map[string]any{"id": "sess-x", "data": "x"}); !scopeRefused(resp) {
		t.Errorf("shortened id as caller: error = %+v, want session_scope", resp.Error)
	}
	if len(pty.Writes()) != 0 {
		t.Fatalf("writes reached the runtime: %v", pty.Writes())
	}
}

// An exited child with no row is no longer folded into the session list, so only
// its holder's record still names the parent, and the parent must be able to
// forget it.
func TestScopeReachesExitedChildWithoutRow(t *testing.T) {
	srv, pty := rulesServer(t, []domain.Session{rulesRow("a", "")},
		ptyruntime.SessionInfo{ID: "a"},
		ptyruntime.SessionInfo{ID: "kid", ParentID: "a", Status: ptyruntime.StatusExited})
	if resp := rulesCall(t, srv, "a", "pty.forget", map[string]any{"id": "kid"}); scopeRefused(resp) {
		t.Fatalf("parent forgetting its exited child: %+v", resp.Error)
	}
	if len(pty.Forgets()) != 1 {
		t.Fatalf("forgets = %v", pty.Forgets())
	}
}

// A child's holder records its parent seconds before the creator saves its row,
// and a session's own row can lag its holder in the same way.
func TestScopeReadsHolderBeforeRowExists(t *testing.T) {
	srv, _ := rulesServer(t, []domain.Session{rulesRow("a", ""), rulesRow("b", "")},
		ptyruntime.SessionInfo{ID: "a"}, ptyruntime.SessionInfo{ID: "b"},
		ptyruntime.SessionInfo{ID: "kid", ParentID: "a"}, ptyruntime.SessionInfo{ID: "unsaved"})
	if resp := rulesCall(t, srv, "a", "pty.input", map[string]any{"id": "kid", "data": "x"}); scopeRefused(resp) {
		t.Errorf("parent on its unsaved child: %+v", resp.Error)
	}
	if resp := rulesCall(t, srv, "b", "pty.input", map[string]any{"id": "kid", "data": "x"}); !scopeRefused(resp) {
		t.Errorf("stranger on the unsaved child: error = %+v, want session_scope", resp.Error)
	}
	if resp := rulesCall(t, srv, "unsaved", "pty.input", map[string]any{"id": "unsaved", "data": "x"}); scopeRefused(resp) {
		t.Errorf("unsaved session on itself: %+v", resp.Error)
	}
}

func TestScopeParentCycleEnds(t *testing.T) {
	srv, _ := rulesServer(t, []domain.Session{rulesRow("a", ""), rulesRow("x", "y"), rulesRow("y", "x")},
		ptyruntime.SessionInfo{ID: "a"}, ptyruntime.SessionInfo{ID: "x"})
	if resp := rulesCall(t, srv, "a", "pty.input", map[string]any{"id": "x", "data": "1"}); !scopeRefused(resp) {
		t.Fatalf("error = %+v, want session_scope", resp.Error)
	}
}

// pty.create replaces an exited holder's directory, so naming an id that exists
// is acting on that session, whatever parent the call claims.
func TestScopeCreateOverExistingID(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		parent  string
		allowed bool
	}{
		{name: "another session's id under the caller", id: "b", parent: "a"},
		{name: "another session's helper under the caller", id: "b-run", parent: "a"},
		{name: "own exited helper again", id: "a-run", allowed: true},
		{name: "own child again", id: "kid", parent: "a", allowed: true},
		{name: "path in the id", id: "a-/../b"},
		{name: "path in the id under the caller", id: "x/../b", parent: "a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, pty := rulesServer(t, []domain.Session{rulesRow("a", ""), rulesRow("b", ""), rulesRow("kid", "a")},
				ptyruntime.SessionInfo{ID: "a"}, ptyruntime.SessionInfo{ID: "b"},
				ptyruntime.SessionInfo{ID: "b-run"}, ptyruntime.SessionInfo{ID: "a-run"})
			params := map[string]any{"id": c.id, "command": "sh"}
			if c.parent != "" {
				params["parent_id"] = c.parent
			}
			resp := rulesCall(t, srv, "a", "pty.create", params)
			if scopeRefused(resp) == c.allowed {
				t.Fatalf("error = %+v, allowed = %v", resp.Error, c.allowed)
			}
			if !c.allowed && len(pty.Creates()) != 0 {
				t.Fatalf("create reached the runtime: %v", pty.Creates())
			}
		})
	}
}
