package main

import (
	"strings"
	"testing"
)

// TestHarnessCmdDirtyReads sends dirty for list and aws plan, and list says
// it read uncommitted changes.
func TestHarnessCmdDirtyReads(t *testing.T) {
	f := &fakeHarness{t: t, answers: map[string][]any{
		"harness.list":     {`{"type":"harness_list","session":"s1","source":"file","sha":"abc","dirty":true,"scenarios":[{"id":"refund","title":"Refund"}],"runs":[]}`},
		"harness.aws_plan": {`{"type":"harness_aws_plan","session":"s1","sha":"abc","dirty":true,"plans":[]}`},
	}}
	env := map[string]string{"AIMAN_ID": "s1"}
	out, _, err := runHarnessTest(t, f, env, "list", "--dirty")
	if err != nil || !strings.Contains(out, "with uncommitted changes") || f.calls[0].params["dirty"] != true {
		t.Fatalf("list --dirty: err %v out %q params %v", err, out, f.calls[0].params)
	}
	if _, _, err := runHarnessTest(t, f, env, "aws", "plan", "--dirty"); err != nil || f.calls[1].params["dirty"] != true {
		t.Fatalf("aws plan --dirty: err %v params %v", err, f.calls[1].params)
	}
}
