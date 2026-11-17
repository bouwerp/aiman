package harness_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/harnesstest"
	"github.com/bouwerp/aiman/internal/shellx"
)

// This test starts a real Bun debug run: the Hono app in oven/bun:1.3-slim
// under Bun's inspector, bun-dap (built by build/bun-dap/build.sh and
// checked against the pin) on the pinned Bun in aiman's adapter image, and
// a busybox edge. It runs only with AIMAN_TEST_DOCKER=1.

func TestBunBreakpointInTypeScript(t *testing.T) {
	r := harnesstest.UpBunDebugRun(t)

	// The probe serve runs before the app accepts the image's Bun.
	probe := r.Spec.Debug.Probe
	out, err := r.Exec(harness.ServiceApp, probe.Command[len(probe.Command)-1])
	if err != nil {
		t.Fatalf("probe: %v: %s", err, out)
	}
	if err := probe.Check(out); err != nil {
		t.Fatalf("probe refused %s: %v\n%s", harnesstest.BunImage, err, out)
	}

	// The inspector answers only under the run's prefix.
	inspector := r.AppDebugIP + ":" + strconv.Itoa(harness.BunInspectPort)
	get := func(path string) string {
		script := `exec 3<>/dev/tcp/` + strings.Replace(inspector, ":", "/", 1) +
			`; printf "GET ` + path + ` HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n" >&3; head -n 1 <&3`
		out, _ := r.Exec(harness.ServiceAdapter, "timeout 5 bash -c "+shellx.Quote(script))
		return out
	}
	if status := get("/wrong-prefix"); !strings.Contains(status, "404") {
		t.Fatalf("inspector without the prefix answered %q, want 404", status)
	}

	tr := harnesstest.DialLease(t, r.HostPort, "harness-test-lease-bun-0123456789")
	path, s := harnesstest.HitBunBreakpoint(t, tr, r.Launch.Arguments, "/src/src/index.ts")
	if path != "/src/src/index.ts" {
		t.Fatalf("frame source %q, want /src/src/index.ts\n%s", path, r.Logs())
	}
	// The paused frame is a real debugger frame: c is in scope.
	st := s.Must("threads", map[string]any{})
	if threads, _ := st["body"].(map[string]any)["threads"].([]any); len(threads) == 0 {
		t.Fatalf("no threads: %v", st)
	}
}
