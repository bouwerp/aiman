package server

import (
	"testing"

	"github.com/bouwerp/aiman/internal/harness"
)

// TestHarnessAWSRouting checks dispatch, scope and refusals of harness.aws;
// the reads themselves are covered in internal/harness.
func TestHarnessAWSRouting(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{})
	wantHarnessCode(t, "no run", rpc(t, srv, "s1", "harness.aws", map[string]any{"session": "s1", "op": "s3_buckets"}), harness.CodeNotRunning)
	run := harnessUp(t, srv, "s1", map[string]any{"session": "s1", "scenario": "plain", "wait": true})
	wantHarnessCode(t, "no op", rpc(t, srv, "s1", "harness.aws", map[string]any{"run": run.ID}), CodeInvalidParams)
	wantHarnessCode(t, "unknown op", rpc(t, srv, "s1", "harness.aws", map[string]any{"run": run.ID, "op": "s3_put"}), CodeInvalidParams)
	wantHarnessCode(t, "no aws service", rpc(t, srv, "s1", "harness.aws", map[string]any{"run": run.ID, "op": "s3_buckets"}), CodeInvalidParams)
	wantHarnessCode(t, "other session", rpc(t, srv, "s2", "harness.aws", map[string]any{"run": run.ID, "op": "s3_buckets"}), CodeSessionScope)
	wantHarnessCode(t, "bad key", rpc(t, srv, "s1", "harness.aws", map[string]any{"run": run.ID, "op": "ddb_get", "key": []int{1}}), CodeInvalidParams)
	wantHarnessCode(t, "bad params", rpc(t, srv, "s1", "harness.aws", map[string]any{"run": run.ID, "op": "s3_objects", "limit": "x"}), CodeInvalidParams)
}
