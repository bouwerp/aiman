package cgroup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAdoptArgv(t *testing.T) {
	got := strings.Join(adoptArgv("ab c", Limits{MemoryHigh: 6 << 30, MemoryMax: Infinity, TasksMax: 512}, []int{41, 42}), " ")
	want := "--user call org.freedesktop.systemd1 /org/freedesktop/systemd1 org.freedesktop.systemd1.Manager " +
		"StartTransientUnit ssa(sv)a(sa(sv)) aiman-session-ab_c.scope fail 7 " +
		"PIDs au 2 41 42 Slice s aiman-sessions.slice Description s aiman session ab c " +
		"CollectMode s inactive-or-failed MemoryHigh t 6442450944 MemoryMax t 18446744073709551615 TasksMax t 512 0"
	if got != want {
		t.Fatalf("adoptArgv:\n got %s\nwant %s", got, want)
	}
}

func TestAdoptWithNothingOutsideCallsNothing(t *testing.T) {
	sd := NewSystemd(t.TempDir(), func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("systemd was called")
		return nil, nil
	})
	// A pid that is gone is neither moved nor an error.
	moved, err := sd.Adopt(context.Background(), "a", Limits{MemoryHigh: 1 << 30}, func() []int { return []int{1 << 30} })
	if moved != 0 || err != nil {
		t.Fatalf("Adopt = %d, %v", moved, err)
	}
}

func TestAdoptReportsAFailedStart(t *testing.T) {
	sd := NewSystemd(t.TempDir(), func(context.Context, string, ...string) ([]byte, error) {
		return []byte("Call failed: no bus\nmore"), os.ErrPermission
	})
	_, err := sd.Adopt(context.Background(), "a", Limits{}, func() []int { return []int{os.Getpid()} })
	if err == nil || !strings.Contains(err.Error(), "Call failed: no bus") {
		t.Fatalf("err = %v", err)
	}
}

// Running processes move into a new scope with its limits, a second call
// moves nothing, and a process started later joins the existing scope.
func TestAdoptMovesRunningProcesses(t *testing.T) {
	sd, err := Probe(context.Background())
	if err != nil {
		t.Skipf("no user systemd: %v", err)
	}
	if _, err := exec.LookPath("busctl"); err != nil {
		t.Skip("no busctl")
	}
	if !strings.HasPrefix(Of(os.Getpid()), sd.Root+"/") {
		t.Skip("this process is outside the user manager's subtree")
	}
	start := func() int {
		cmd := exec.Command("tail", "-f", "/dev/null")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return cmd.Process.Pid
	}
	id := "adopttest-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	limits := Limits{MemoryHigh: 3 << 30, MemoryMax: 4 << 30, TasksMax: 512}
	pids := []int{start(), start()}
	list := func() []int { return pids }

	moved, err := sd.Adopt(context.Background(), id, limits, list)
	if err != nil || moved != 2 {
		t.Fatalf("Adopt = %d, %v", moved, err)
	}
	dir := sd.ScopeDir(id)
	for _, pid := range pids {
		if Of(pid) != dir {
			t.Fatalf("pid %d is in %s, want %s", pid, Of(pid), dir)
		}
	}
	for file, want := range map[string]string{"memory.high": "3221225472", "memory.max": "4294967296", "pids.max": "512"} {
		raw, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil || strings.TrimSpace(string(raw)) != want {
			t.Fatalf("%s = %q, %v; want %s", file, raw, err, want)
		}
	}
	if moved, err := sd.Adopt(context.Background(), id, limits, list); err != nil || moved != 0 {
		t.Fatalf("second Adopt = %d, %v", moved, err)
	}

	late := start()
	pids = append(pids, late)
	if moved, err := sd.Adopt(context.Background(), id, limits, list); err != nil || moved != 1 {
		t.Fatalf("late Adopt = %d, %v", moved, err)
	}
	if Of(late) != dir {
		t.Fatalf("late pid is in %s, want %s", Of(late), dir)
	}
}
