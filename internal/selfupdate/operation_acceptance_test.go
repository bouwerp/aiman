//go:build unix

package selfupdate_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/bouwerp/aiman/internal/selfupdate"
)

// Acceptance tests for daemon-control piece A (restart as a persisted
// operation of the self-update manager). Do not edit.

func opHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, "v0.47.0", selfupdate.ModeAuto)
	h.m.OpsDir = filepath.Join(t.TempDir(), "ops")
	if err := os.MkdirAll(h.m.OpsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	h.m.OpScript = func(name string) string { return "#!/bin/sh\n# " + name + "\n" }
	return h
}

func opResult(h *harness, name string) string {
	return filepath.Join(h.m.OpsDir, name+".result.json")
}

func TestOperationStartsAndPersists(t *testing.T) {
	h := opHarness(t)
	out := h.m.StartOperation("restart-gateway", false)
	if !out.Accepted || out.Target != "restart-gateway" {
		t.Fatalf("outcome = %+v", out)
	}
	if got := h.runner.started(); !slices.Equal(got, []string{"restart-gateway"}) {
		t.Fatalf("runner got %v", got)
	}
	op := h.m.Status().Operation
	if op == nil || op.Kind != selfupdate.AttemptKindRestart || op.To != "restart-gateway" || op.Status != selfupdate.AttemptStarted {
		t.Fatalf("status operation = %+v", op)
	}
	// Persisted, so a serve that comes back reads it.
	if s := h.state(t); s.Operation == nil || s.Operation.To != "restart-gateway" {
		t.Fatalf("state operation = %+v", s.Operation)
	}
}

func TestOperationRefusesUnknownNamesAndBusy(t *testing.T) {
	h := opHarness(t)
	for _, name := range []string{"", "restart-sshd", "v0.48.0", "../restart-serve"} {
		if out := h.m.StartOperation(name, true); out.Accepted {
			t.Fatalf("%q accepted", name)
		}
	}
	busy := 1
	h.m.Busy = []func() int{func() int { return busy }}
	if out := h.m.StartOperation("restart-serve", false); out.Accepted || out.Reason != "busy" {
		t.Fatalf("busy outcome = %+v", out)
	}
	if out := h.m.StartOperation("restart-serve", true); !out.Accepted {
		t.Fatalf("forced outcome = %+v", out)
	}
	if got := h.runner.started(); !slices.Equal(got, []string{"restart-serve"}) {
		t.Fatalf("runner got %v", got)
	}
}

func TestOperationBlocksAnotherOperationAndUpdates(t *testing.T) {
	h := opHarness(t)
	setAlive(t, func(int) bool { return true })
	h.fetcher.set(selfupdate.Release{Tag: "v0.48.0", Ready: true}, true)
	if out := h.m.StartOperation("restart-serve", false); !out.Accepted {
		t.Fatalf("outcome = %+v", out)
	}
	if out := h.m.StartOperation("restart-gateway", true); out.Accepted || out.Reason != "running" {
		t.Fatalf("second operation = %+v", out)
	}
	// A serve that came back mid-restart: a fresh manager over the same files.
	fresh := &selfupdate.Manager{
		Version: h.m.Version, Fetcher: h.fetcher, Runner: h.runner, Script: testScript,
		StatePath: h.m.StatePath, ResultPath: h.m.ResultPath, OpsDir: h.m.OpsDir, OpScript: h.m.OpScript,
		Policy: h.m.Policy, Logf: t.Logf, Now: h.m.Now,
	}
	if out := fresh.Request(context.Background()); out.Accepted {
		t.Fatalf("update accepted while a restart is started: %+v", out)
	}
	fresh.Poll(context.Background())
	if got := h.runner.started(); slices.Contains(got, "v0.48.0") {
		t.Fatalf("auto-update started during a restart: %v", got)
	}
}

func TestOperationSettlesFromItsOwnResult(t *testing.T) {
	h := opHarness(t)
	// An earlier serve install stays the update attempt.
	h.seed(t, selfupdate.State{Attempt: &selfupdate.Attempt{At: testNow, From: "v0.46.0", To: "v0.47.0", Status: selfupdate.AttemptOK}})
	if out := h.m.StartOperation("restart-gateway", false); !out.Accepted {
		t.Fatalf("outcome = %+v", out)
	}
	writeResultFile(t, opResult(h, "restart-gateway"), "restart-gateway", 3, testNow)
	h.m.Poll(context.Background())
	st := h.m.Status()
	if st.Operation == nil || st.Operation.Status != selfupdate.AttemptFailed || st.Operation.Exit != 3 {
		t.Fatalf("operation = %+v", st.Operation)
	}
	if st.Attempt == nil || st.Attempt.To != "v0.47.0" || st.Attempt.Status != selfupdate.AttemptOK {
		t.Fatalf("update attempt changed: %+v", st.Attempt)
	}
	if _, err := os.Stat(h.m.ResultPath); !os.IsNotExist(err) {
		t.Fatalf("operation wrote the update result file: %v", err)
	}
	// Settled, so the next operation may start; this one succeeds.
	if out := h.m.StartOperation("restart-gateway", false); !out.Accepted {
		t.Fatalf("outcome after settle = %+v", out)
	}
	writeResultFile(t, opResult(h, "restart-gateway"), "restart-gateway", 0, testNow)
	h.m.Poll(context.Background())
	if op := h.m.Status().Operation; op == nil || op.Status != selfupdate.AttemptOK {
		t.Fatalf("operation = %+v", op)
	}
}

func TestScriptRunnerWritesOperationResultsApart(t *testing.T) {
	d := newRunnerDirs(t)
	r := selfupdate.NewScriptRunner(d.tmp, d.log, d.result)
	r.OpsDir = filepath.Join(filepath.Dir(d.result), "ops")
	if _, err := r.Start("restart-bogus", "exit 0\n"); err == nil {
		t.Fatal("free-form operation name accepted")
	}
	if _, err := r.Start("restart-trigger", "exit 4\n"); err != nil {
		t.Fatal(err)
	}
	res := waitResult(t, filepath.Join(r.OpsDir, "restart-trigger.result.json"))
	if res.Tag != "restart-trigger" || res.Exit != 4 {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Stat(d.result); !os.IsNotExist(err) {
		t.Fatalf("operation wrote the update result file: %v", err)
	}
}
