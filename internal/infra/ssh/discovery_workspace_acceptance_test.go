package ssh

import (
	"strings"
	"testing"
)

// Acceptance test for multi-repo sessions, piece P1. Children must not edit
// this file.

// The batch scan reports the aiman-workspace marker as a sixth field.
func TestParseWorktreeRecordsWorkspace(t *testing.T) {
	out := "WT\t/repos/aiman\t/repos/host-tab@ws/aiman\tok\t\tws-1\n" +
		"WT\t/repos/aiman\t/repos/aiman@x\tok\ts-2\t\n" +
		"WT\t/repos/aiman\t/repos/aiman@y\tok\ts-3\n" // an older five-field line
	recs := parseWorktreeRecords(out)
	if len(recs) != 3 {
		t.Fatalf("records = %+v", recs)
	}
	if recs[0].Workspace != "ws-1" || recs[0].AimanID != "" {
		t.Errorf("rec0 = %+v", recs[0])
	}
	if recs[1].Workspace != "" || recs[1].AimanID != "s-2" {
		t.Errorf("rec1 = %+v", recs[1])
	}
	if recs[2].AimanID != "s-3" {
		t.Errorf("rec2 = %+v", recs[2])
	}
	if !strings.Contains(worktreeScanScript("/repos"), "aiman-workspace") {
		t.Error("scan script does not read aiman-workspace")
	}
}
