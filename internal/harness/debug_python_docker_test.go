package harness_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/harnesstest"
)

// These tests start real Python debug runs: debugpy from the pinned wheel in
// the app container (no adapter container) and a busybox edge. They run only
// with AIMAN_TEST_DOCKER=1.

func runPythonProbe(t *testing.T, r *harnesstest.DebugRun) string {
	t.Helper()
	probe := r.Spec.Debug.Probe
	out, err := r.Exec(harness.ServiceApp, probe.Command[len(probe.Command)-1])
	if err != nil {
		t.Fatalf("probe: %v: %s", err, out)
	}
	if err := probe.Check(out); err != nil {
		t.Fatalf("probe refused: %v\n%s", err, out)
	}
	return out
}

func TestDebugpyBreakpoint(t *testing.T) {
	r := harnesstest.UpPythonDebugRun(t)
	runPythonProbe(t, r)

	s := harnesstest.NewSession(t, harnesstest.DialLease(t, r.HostPort, "harness-test-lease-python-0123456789"))
	s.Must("initialize", harnesstest.PythonInit)
	s.Start("attach", r.Launch.Arguments)
	s.Event("initialized")
	top := harnesstest.StopAt(t, s, "/src/app.py", harnesstest.PythonBreakLine)
	if top["name"] != "bump" {
		t.Fatalf("top frame %v, want bump\n%s", top, r.Logs())
	}
	src, _ := top["source"].(map[string]any)
	if src["path"] != "/src/app.py" {
		t.Fatalf("frame source %v, want /src/app.py", src["path"])
	}

	// Another client without the lease is refused while this one holds it.
	other := harnesstest.NewSession(t, harnesstest.DialDAP(t, r.HostPort))
	if m, err := other.Request("initialize", harnesstest.PythonInit); err == nil && m["success"] == true {
		t.Fatalf("a second client without the lease was admitted: %v", m)
	}
}

func TestDebugpyWrappedChild(t *testing.T) {
	r := harnesstest.UpPythonWrapRun(t)
	out := runPythonProbe(t, r)
	if !strings.Contains(out, "AIMAN_MUSL=1") {
		t.Fatalf("%s is not musl, probe said %q", harnesstest.PythonMuslImage, out)
	}

	const lease = "harness-test-lease-python-wrap-0123456789"
	root := harnesstest.NewSession(t, harnesstest.DialLease(t, r.HostPort, lease))
	root.Must("initialize", harnesstest.PythonInit)
	root.Start("attach", r.Launch.Arguments)
	root.Event("initialized")
	top := harnesstest.StopAt(t, root, "/src/child.py", harnesstest.PythonChildLine)
	if top["name"] != "work" {
		t.Fatalf("top frame %v, want work in the wrapped child\n%s", top, r.Logs())
	}

	// child.py started grandchild.py through the wrapper too; debugpy
	// reports it with startDebugging and it joins under the same lease.
	sd := root.ReverseRequest("startDebugging")
	args, _ := sd["arguments"].(map[string]any)
	cfg, _ := args["configuration"].(map[string]any)
	if cfg["subProcessId"] == nil {
		t.Fatalf("startDebugging without a subprocess id: %v", sd)
	}
	child := harnesstest.NewSession(t, harnesstest.DialLease(t, r.HostPort, lease))
	child.Must("initialize", harnesstest.PythonInit)
	child.Start(fmt.Sprint(args["request"]), cfg)
	child.Event("initialized")
	top = harnesstest.StopAt(t, child, "/src/grandchild.py", harnesstest.PythonGrandchildLine)
	if top["name"] != "leaf" {
		t.Fatalf("top frame %v, want leaf in the subprocess\n%s", top, r.Logs())
	}
}
