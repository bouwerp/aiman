package harness_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/harnesstest"
)

// TestVitestBreakpointInTest runs a node debug run whose scenario has a
// Vitest 4 check, debugs the check (its own container and js-debug
// adapter, the relay switched to it), stops at a breakpoint in the .ts
// test, lets it finish, and sees the relay back on the app. It runs only
// with AIMAN_TEST_DOCKER=1.
func TestVitestBreakpointInTest(t *testing.T) {
	m := harnesstest.NewVitestManager(t, 5*time.Minute)
	repo := harnesstest.VitestRepo(t)
	ctx := context.Background()
	run, err := m.Up(ctx, harness.UpRequest{Session: "s1", Repo: repo, Scenario: "dbg", Debug: true, Actor: domain.EventActorUser})
	if err != nil {
		t.Fatal(err)
	}
	project := "harness-" + run.ID
	t.Cleanup(func() {
		_ = exec.Command("docker", "compose", "-p", project, "--profile", "*", "down", "-v", "--remove-orphans", "-t", "1").Run()
	})
	logs := func() string {
		out, _ := exec.Command("docker", "compose", "-p", project, "logs", "--no-color", "--tail", "60").CombinedOutput()
		return string(out)
	}
	run = m.Wait(ctx, run.ID, 5*time.Minute)
	if run.State != harness.StateReady {
		t.Fatalf("run %s: %s: %s", run.State, run.FailedStep, run.Error)
	}
	port := 0
	for _, p := range run.Ports {
		if p.Name == harness.RelayDAP {
			port = p.HostPort
		}
	}

	if _, err := m.DebugCheck(ctx, harness.DebugCheckRequest{Run: run.ID, Name: "sum", Actor: domain.EventActorUser}); err != nil {
		t.Fatalf("debug check: %v\n%s", err, logs())
	}
	run, _ = m.Get(run.ID)
	if run.DebugCheck == nil || run.DebugCheck.Launch == nil {
		t.Fatalf("debug check %+v", run.DebugCheck)
	}
	launch := run.DebugCheck.Launch
	dial := func() harnesstest.Transport { return harnesstest.DialLease(t, port, testLease) }
	path, child := harnesstest.HitJSBreakpoint(t, dial, launch.Request, launch.Arguments, "/src/tests/sum.test.ts", harnesstest.VitestBreakLine)
	if path != "/src/tests/sum.test.ts" {
		t.Fatalf("frame source %q, want the .ts test\n%s", path, logs())
	}
	st, err := m.Debug(ctx, run.ID, harness.DebugStatus, domain.EventActorUser)
	if err != nil || st.Target != harness.TargetCheck || st.Holder == nil {
		t.Fatalf("debug status while stopped: %+v %v", st, err)
	}
	// The watcher sees the client within its one-second poll.
	for i := 0; ; i++ {
		r, _ := m.Get(run.ID)
		if r.DebugCheck != nil && r.DebugCheck.State == harness.DebugCheckAttached {
			break
		}
		if i == 50 {
			t.Fatalf("the check never showed attached: %+v", r.DebugCheck)
		}
		time.Sleep(100 * time.Millisecond)
	}
	child.Must("continue", map[string]any{"threadId": 0})

	var c harness.CheckRun
	deadline := time.Now().Add(2 * time.Minute)
	for {
		r, _ := m.Get(run.ID)
		if r.DebugCheck == nil {
			for _, x := range r.Checks {
				if x.Name == "sum" {
					c = x
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the debug check never ended\n%s", logs())
		}
		time.Sleep(500 * time.Millisecond)
	}
	if c.Exit == nil || *c.Exit != 0 || !c.Debug || !strings.Contains(c.Output, "passed") {
		t.Fatalf("check result %+v\n%s", c, logs())
	}
	st, err = m.Debug(ctx, run.ID, harness.DebugStatus, domain.EventActorUser)
	if err != nil || st.Target != harness.TargetApp || st.Launch == nil || st.Launch.Arguments["address"] == launch.Arguments["address"] {
		t.Fatalf("debug status after the check: %+v %v", st, err)
	}
	if err := m.Down(ctx, run.ID, harness.StopDown); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("docker", "ps", "-a", "-q", "--filter", "label=com.docker.compose.project="+project).CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("containers left after down: %v %s", err, out)
	}
}
