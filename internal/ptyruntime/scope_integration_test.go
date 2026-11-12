//go:build linux

package ptyruntime

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/cgroup"
)

// scopeSystemd returns the host's user systemd, or skips the test.
func scopeSystemd(t *testing.T) *cgroup.Systemd {
	t.Helper()
	sd, err := cgroup.Probe(context.Background())
	if err != nil {
		t.Skipf("no user systemd: %v", err)
	}
	return sd
}

func procCgroup(t *testing.T, pid int) string {
	t.Helper()
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
	if err != nil {
		t.Fatalf("read cgroup of %d: %v", pid, err)
	}
	return strings.TrimSpace(string(raw))
}

func cgroupValue(t *testing.T, dir, file string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	return strings.TrimSpace(string(raw))
}

// A holder serve spawns runs in its session's scope with the configured
// limits, the agent's processes inherit it, a second terminal of the session
// joins it, and a new manager over the same root (a restarted serve) finds
// the holder alive.
func TestHolderRunsInSessionScope(t *testing.T) {
	sd := scopeSystemd(t)
	root := shortTempDir(t)
	m := newTestManagerAt(t, root, append([]string(nil), holderBin...))
	m.Scopes = sd

	id := "scopetest-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	limits := cgroup.Limits{MemoryHigh: 3 << 30, MemoryMax: 4 << 30, TasksMax: 512}
	info, err := m.Create(Spec{ID: id, Dir: root, Command: "sleep 300", Limits: limits})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	unit := cgroup.ScopeName(id)
	if got := procCgroup(t, info.PID); !strings.HasSuffix(got, "/"+cgroup.SessionsSlice+"/"+unit) {
		t.Fatalf("agent cgroup = %q, want it to end in %s/%s", got, cgroup.SessionsSlice, unit)
	}
	dir := sd.ScopeDir(id)
	if got := cgroupValue(t, dir, "memory.high"); got != strconv.Itoa(3<<30) {
		t.Errorf("memory.high = %s, want %d", got, 3<<30)
	}
	if got := cgroupValue(t, dir, "memory.max"); got != strconv.Itoa(4<<30) {
		t.Errorf("memory.max = %s, want %d", got, 4<<30)
	}
	if got := cgroupValue(t, dir, "pids.max"); got != "512" {
		t.Errorf("pids.max = %s, want 512", got)
	}

	// A holder joins an existing scope by writing its own pid, which the
	// kernel allows only inside the user manager's delegated subtree: serve
	// under its systemd unit. A test runner started elsewhere (a system
	// service, a login session scope) cannot, and the holder then stays put.
	own, _ := os.ReadFile("/proc/self/cgroup")
	if !strings.Contains(string(own), strings.TrimPrefix(sd.Root, "/sys/fs/cgroup")+"/") {
		t.Logf("not under the user manager (%s): skipping the join and restart checks", strings.TrimSpace(string(own)))
		_ = m.Kill(id)
		return
	}

	// A helper terminal of the same session joins the scope and may carry
	// new limits for it.
	helper, err := m.Create(Spec{ID: id + "-run", Dir: root, Command: "sleep 300", ScopeKey: id,
		Limits: cgroup.Limits{MemoryHigh: 2 << 30}})
	if err != nil {
		t.Fatalf("Create helper: %v", err)
	}
	if got := procCgroup(t, helper.PID); !strings.HasSuffix(got, "/"+unit) {
		t.Fatalf("helper cgroup = %q, want it in %s", got, unit)
	}
	if got := cgroupValue(t, dir, "memory.high"); got != strconv.Itoa(2<<30) {
		t.Errorf("memory.high after the helper joined = %s, want %d", got, 2<<30)
	}

	// A restarted serve is a new manager over the same root.
	m.CloseAll()
	m2 := newTestManagerAt(t, root, append([]string(nil), holderBin...))
	got, err := m2.Get(id)
	if err != nil || got.Status != StatusRunning {
		t.Fatalf("holder after a manager restart: %+v, %v", got, err)
	}

	// The scope goes away with its last process.
	if err := m2.Kill(id); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if err := m2.Kill(id + "-run"); err != nil {
		t.Fatalf("Kill helper: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for sd.Active(id) && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if sd.Active(id) {
		t.Errorf("scope %s still exists after both terminals were killed", unit)
	}
}

type brokenScoper struct{}

func (brokenScoper) Launch(string, cgroup.Limits) ([]string, string) {
	return []string{"false", "--"}, ""
}

// A launcher that fails must not cost the session: the holder starts plainly.
func TestCreateFallsBackWhenScopeLaunchFails(t *testing.T) {
	m := newTestManager(t)
	m.Scopes = brokenScoper{}
	info, err := m.Create(Spec{ID: "fallback", Dir: t.TempDir(), Command: "sleep 300"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.Status != StatusRunning {
		t.Fatalf("status = %s", info.Status)
	}
}
