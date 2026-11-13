//go:build unix

package selfupdate_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bouwerp/aiman/internal/selfupdate"
)

func TestOperationSettlesWithUpdatesOff(t *testing.T) {
	h := opHarness(t)
	h.m.Policy.Mode = selfupdate.ModeOff
	if out := h.m.StartOperation(selfupdate.OpRestartTrigger, false); !out.Accepted {
		t.Fatalf("outcome = %+v", out)
	}
	writeResultFile(t, opResult(h, selfupdate.OpRestartTrigger), selfupdate.OpRestartTrigger, 0, testNow)
	h.m.Finish(context.Background())
	if op := h.m.Status().Operation; op == nil || op.Status != selfupdate.AttemptOK {
		t.Fatalf("operation = %+v", op)
	}
}

func TestOperationRefusedWhileInstallRuns(t *testing.T) {
	h := opHarness(t)
	setAlive(t, func(int) bool { return true })
	h.seed(t, selfupdate.State{Attempt: &selfupdate.Attempt{At: testNow, From: "v0.47.0", To: "v0.48.0", Status: selfupdate.AttemptStarted, PID: fakePID}})
	if out := h.m.StartOperation(selfupdate.OpRestartServe, true); out.Accepted || out.Reason != selfupdate.ReasonRunning {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestScriptRunnerRefusesOperationsWithoutOpsDir(t *testing.T) {
	d := newRunnerDirs(t)
	r := selfupdate.NewScriptRunner(d.tmp, d.log, d.result)
	if _, err := r.Start(selfupdate.OpRestartServe, "exit 0\n"); err == nil {
		t.Fatal("operation started without an operations directory")
	}
	if got := selfupdate.OperationResultPath("/x/ops", selfupdate.OpRestartServe); got != filepath.Join("/x/ops", "restart-serve.result.json") {
		t.Fatalf("result path %s", got)
	}
}
