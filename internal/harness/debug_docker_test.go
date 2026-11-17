package harness_test

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/harnesstest"
)

// These tests start a real debug run: the pinned Delve built on this host,
// a busybox app and edge, and a postgres service. They run only with
// AIMAN_TEST_DOCKER=1.

func TestDAPBreakpointHit(t *testing.T) {
	r := harnesstest.UpDebugRun(t)
	tr := harnesstest.DialDAP(t, r.HostPort)
	s := harnesstest.NewSession(t, tr)
	path := harnesstest.HitBreakpoint(t, s, r.Launch)
	if path != "/src/main.go" {
		t.Fatalf("frame source %q, want /src/main.go", path)
	}

	// While this client holds the debugger, another is refused with a DAP
	// error naming the holder, and never reaches dlv.
	other := harnesstest.NewSession(t, harnesstest.DialDAP(t, r.HostPort))
	resp, err := other.Request("initialize", map[string]any{"clientID": "vscode", "clientName": "Visual Studio Code", "adapterID": "go"})
	if err != nil {
		t.Fatalf("second client got no answer: %v", err)
	}
	if resp["success"] != false || !strings.Contains(resp["message"].(string), "harness test") {
		t.Fatalf("second client = %v", resp)
	}

	// Leaving frees the lease; dlv restarts and the next client can debug.
	_ = tr.C.Close()
	deadline := time.Now().Add(30 * time.Second)
	for {
		next := harnesstest.NewSession(t, harnesstest.DialDAP(t, r.HostPort))
		resp, err := next.Request("initialize", map[string]any{"clientID": "test", "clientName": "harness test", "adapterID": "go"})
		if err == nil && resp["success"] == true {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no client admitted after the holder left: %v %v\n%s", resp, err, r.Logs())
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func TestServiceCannotReachDAP(t *testing.T) {
	r := harnesstest.UpDebugRun(t)
	// The edge reaches dlv on the app's debug address: the control case.
	if out, err := r.Exec(harness.ServiceEdge, "nc -w 3 "+r.AppDebugIP+" 2345 </dev/null"); err != nil {
		t.Fatalf("edge cannot reach the DAP port: %v: %s\n%s", err, out, r.Logs())
	}
	// The service reaches neither the debug address nor the app by name.
	for _, target := range []string{r.AppDebugIP, harness.ServiceApp} {
		probe := "timeout 4 bash -c 'echo > /dev/tcp/" + target + "/2345'"
		if out, err := r.Exec("db", probe); err == nil {
			t.Fatalf("service reached the DAP port at %s: %s", target, out)
		}
	}
	// And it is on no debug network at all.
	out, err := exec.Command("docker", "inspect", "-f", "{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}",
		r.Project.Name+"-db-1").CombinedOutput()
	if err != nil {
		t.Fatalf("inspect db: %v: %s", err, out)
	}
	if strings.Contains(string(out), "_"+harness.NetworkDebug) {
		t.Fatalf("db is on the debug network: %s", out)
	}
	nets, err := exec.Command("docker", "network", "inspect", "-f", "{{.Internal}}", r.Project.Name+"_"+harness.NetworkDebug).CombinedOutput()
	if err != nil || strings.TrimSpace(string(nets)) != "true" {
		t.Fatalf("debug network internal = %s, %v", nets, err)
	}
}
