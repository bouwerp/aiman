package server

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/harness"
)

// TestAWSWriteNeedsOperator: a session caller is refused before the run is
// looked up, so a session cannot learn about a run by trying to write.
func TestAWSWriteNeedsOperator(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{})
	run := harnessUp(t, srv, "s1", map[string]any{"session": "s1", "scenario": "plain", "wait": true})
	write := map[string]any{"run": run.ID, "op": "s3_put", "bucket": "uploads", "key": "a", "data": "aGk="}
	wantHarnessCode(t, "session write", rpc(t, srv, "s1", "harness.aws_write", write), CodeNeedsOperator)
	wantHarnessCode(t, "session reset", rpc(t, srv, "s1", "harness.aws_write", map[string]any{"run": run.ID, "op": "reset"}), CodeNeedsOperator)
	write["run"] = "nope"
	wantHarnessCode(t, "session write to an unknown run", rpc(t, srv, "s1", "harness.aws_write", write), CodeNeedsOperator)
	wantHarnessCode(t, "session write with bad params", rpc(t, srv, "s1", "harness.aws_write", map[string]any{"op": 3}), CodeNeedsOperator)
	if rows := ledgerRows(t, srv, domain.EventHarnessAWSWrite); len(rows) != 0 {
		t.Fatalf("a refused write was recorded: %v", rows)
	}
}

// TestAWSWriteRouting checks the operator's refusals; the writes themselves
// are covered in internal/harness.
func TestAWSWriteRouting(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{})
	run := harnessUp(t, srv, "s1", map[string]any{"session": "s1", "scenario": "plain", "wait": true})
	wantHarnessCode(t, "no op", rpc(t, srv, "", "harness.aws_write", map[string]any{"run": run.ID}), CodeInvalidParams)
	wantHarnessCode(t, "bad data", rpc(t, srv, "", "harness.aws_write", map[string]any{"run": run.ID, "op": "s3_put", "data": "%%"}), CodeInvalidParams)
	wantHarnessCode(t, "bad key", rpc(t, srv, "", "harness.aws_write", map[string]any{"run": run.ID, "op": "ddb_delete", "key": []int{1}}), CodeInvalidParams)
	wantHarnessCode(t, "unknown op", rpc(t, srv, "", "harness.aws_write", map[string]any{"run": run.ID, "op": "s3_copy"}), CodeInvalidParams)
	wantHarnessCode(t, "no aws service", rpc(t, srv, "", "harness.aws_write", map[string]any{"run": run.ID, "op": "reset"}), CodeInvalidParams)
	wantHarnessCode(t, "unknown run", rpc(t, srv, "", "harness.aws_write", map[string]any{"run": "nope", "op": "reset"}), harness.CodeNotRunning)
	if d := callDeadline("harness.aws_write"); d < HarnessAWSResetTimeout {
		t.Fatalf("client deadline %v is shorter than a reset", d)
	}
}
