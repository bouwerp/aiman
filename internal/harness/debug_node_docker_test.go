package harness_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/harnesstest"
)

// These tests start a real Node debug run: the pinned Node and js-debug
// fetched through the tools store, aiman's adapter image as the app (no
// node of its own) and as the adapter, a busybox edge and a postgres
// service. They run only with AIMAN_TEST_DOCKER=1.

const testLease = "harness-test-lease-0123456789"

func TestNodeBreakpointInTypeScript(t *testing.T) {
	r := harnesstest.UpNodeDebugRun(t)
	path, child := harnesstest.HitTSBreakpoint(t, r.HostPort, testLease, r.Launch)
	if path != "/src/src/app.ts" {
		t.Fatalf("frame source %q, want /src/src/app.ts\n%s", path, r.Logs())
	}
	// The child session is a real debugger session: the paused frame has
	// tick's argument in scope.
	st := child.Must("threads", map[string]any{})
	threads, _ := st["body"].(map[string]any)["threads"].([]any)
	if len(threads) == 0 {
		t.Fatalf("no threads: %v", st)
	}

	// While the lease holds two connections, a client with another token, or
	// none, is refused and never reaches js-debug.
	for name, tr := range map[string]*harnesstest.TCPTransport{
		"other token": harnesstest.DialLease(t, r.HostPort, "someone-else-0123456789"),
		"no lease":    harnesstest.DialDAP(t, r.HostPort),
	} {
		resp, err := harnesstest.NewSession(t, tr).Request("initialize", map[string]any{"clientID": "vscode", "clientName": "Visual Studio Code", "adapterID": "pwa-node"})
		if err != nil || resp["success"] != false || !strings.Contains(resp["message"].(string), "harness test") {
			t.Fatalf("%s: %v %v", name, resp, err)
		}
	}
}

func TestServiceCannotReachInspector(t *testing.T) {
	r := harnesstest.UpNodeDebugRun(t)
	inspector := r.AppDebugIP + "/" + strconv.Itoa(harness.NodeInspectPort)
	dap := r.AdapterDebugIP + "/" + strconv.Itoa(harness.JSDebugPort)
	probe := func(target string) string { return "timeout 4 bash -c 'echo > /dev/tcp/" + target + "'" }
	// The adapter reaches the app's inspector: the control case.
	if out, err := r.Exec(harness.ServiceAdapter, probe(inspector)); err != nil {
		t.Fatalf("the adapter cannot reach the inspector: %v: %s\n%s", err, out, r.Logs())
	}
	// A service reaches neither the inspector nor the adapter's DAP port,
	// by address or by name.
	for _, target := range []string{inspector, dap, harness.ServiceApp + "/" + strconv.Itoa(harness.NodeInspectPort), harness.ServiceAdapter + "/" + strconv.Itoa(harness.JSDebugPort)} {
		if out, err := r.Exec("db", probe(target)); err == nil {
			t.Fatalf("service reached %s: %s", target, out)
		}
	}
	// The adapter reaches nothing beyond the debug network.
	if out, err := r.Exec(harness.ServiceAdapter, probe("db/5432")); err == nil {
		t.Fatalf("the adapter reached the db service: %s", out)
	}
}
