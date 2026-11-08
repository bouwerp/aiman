//go:build acceptance

package server

import (
	"context"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func registerParams(id, parent string) map[string]any {
	return map[string]any{"sessions": []domain.Session{{ID: id, Name: id, ParentID: parent}}}
}

func storedParent(t *testing.T, srv *Server, id string) string {
	t.Helper()
	row, err := srv.repo.Get(context.Background(), id)
	if err != nil || row == nil {
		t.Fatalf("row %s: %v", id, err)
	}
	return row.ParentID
}

// F6: a session may not register rows; adopting another session as its child
// would defeat the scope check.
func TestF6RegisterRefusesSessionCaller(t *testing.T) {
	cases := []struct {
		name   string
		caller string
		pid    int
	}{
		{name: "caller", caller: scopeA},
		{name: "holder peer without caller", pid: scopePeer},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := scopeServer(t)
			resp := scopeCall(t, srv, c.caller, c.pid, "session.register", registerParams(scopeB, scopeA))
			if resp.Error == nil || resp.Error.Code != CodeNeedsOperator {
				t.Fatalf("error = %+v, want %s", resp.Error, CodeNeedsOperator)
			}
			if p := storedParent(t, srv, scopeB); p != "" {
				t.Fatalf("sess-b parent = %q after a refused register, want none", p)
			}
		})
	}
}

// F6: the operator (the laptop over SSH, or the gateway) still registers.
func TestF6RegisterOperatorUnchanged(t *testing.T) {
	srv, _ := scopeServer(t)
	resp := scopeCall(t, srv, "", scopeNoPID, "session.register", registerParams("sess-new", ""))
	got := lifecycleResult(t, resp)
	if got["type"] != "sessions_registered" {
		t.Fatalf("result = %v, want sessions_registered", got)
	}
	ids, _ := got["ids"].([]any)
	if len(ids) != 1 || ids[0] != "sess-new" {
		t.Fatalf("ids = %v, want [sess-new]", got["ids"])
	}
}
