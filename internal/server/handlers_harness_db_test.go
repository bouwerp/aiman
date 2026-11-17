package server

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/harness"
)

func TestHarnessDBWriteNeedsOperator(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{})
	run := harnessUp(t, srv, "s1", map[string]any{"session": "s1", "scenario": "pg", "wait": true})
	write := map[string]any{"run": run.ID, "service": "db", "op": "query", "sql": "delete from orders", "write": true}
	wantHarnessCode(t, "session write", rpc(t, srv, "s1", "harness.db", write), CodeNeedsOperator)
	// The refusal comes before the run is looked up, so a session cannot
	// learn about another session's run by trying to write.
	write["run"] = "nope"
	wantHarnessCode(t, "session write to an unknown run", rpc(t, srv, "s1", "harness.db", write), CodeNeedsOperator)
	if rows := ledgerRows(t, srv, domain.EventHarnessDBWrite); len(rows) != 0 {
		t.Fatalf("a refused write was recorded: %v", rows)
	}
}

func TestHarnessDBScopeAndRouting(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{})
	run := harnessUp(t, srv, "s1", map[string]any{"session": "s1", "scenario": "pg", "wait": true})
	read := map[string]any{"run": run.ID, "service": "db", "op": "tables"}
	wantHarnessCode(t, "other session", rpc(t, srv, "s2", "harness.db", read), CodeSessionScope)
	// The service holds no database in this file: the call reached the
	// manager in scope.
	wantHarnessCode(t, "no database", rpc(t, srv, "s1", "harness.db", read), harness.CodeDBUnsupported)
	wantHarnessCode(t, "no service", rpc(t, srv, "s1", "harness.db", map[string]any{"run": run.ID, "op": "tables"}), CodeInvalidParams)
}
