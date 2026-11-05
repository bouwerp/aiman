package server

import "testing"

func TestTodoGateRefusesServeOnlyRef(t *testing.T) {
	srv, _, _ := todoServer(t, todoSession("own"))
	wantCode(t, rpc(t, srv, "", "todo.gate", map[string]any{"session": "own", "ref": "gate:approve"}), CodeInvalidParams)
	wantCode(t, rpc(t, srv, "own", "todo.gate", map[string]any{"session": "own", "ref": "gate:approve"}), CodeInvalidParams)
}
