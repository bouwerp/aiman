package server

import (
	"os"
	"os/exec"
	"runtime"
	"testing"

	"github.com/bouwerp/aiman/internal/ptyruntime"
)

// Only a running record whose PID is still a holder roots the ancestry walk,
// so a stale record whose PID was reused by an unrelated process (an
// operator's shell) does not turn that process into a session.
func TestHolderRoots(t *testing.T) {
	infos := []ptyruntime.SessionInfo{
		{ID: "live", PID: 10, Status: ptyruntime.StatusRunning},
		{ID: "exited", PID: 20, Status: ptyruntime.StatusExited},
		{ID: "reused", PID: 30, Status: ptyruntime.StatusRunning},
		{ID: "nopid", PID: 0, Status: ptyruntime.StatusRunning},
	}
	isHolder := func(pid int) bool { return pid == 10 || pid == 20 }
	roots := holderRoots(infos, isHolder)
	if len(roots) != 1 || !roots[10] {
		t.Fatalf("roots = %v, want only 10", roots)
	}
}

func TestIsHolderCmdline(t *testing.T) {
	for cmdline, want := range map[string]bool{
		"aiman\x00pty\x00hold\x00--root\x00/r\x00--id\x00s1\x00": true,
		"/opt/aiman-dev\x00pty\x00hold\x00--id\x00s1\x00":        true,
		"aiman\x00pty\x00hold\x00":                               false,
		"bash\x00-l\x00":                                         false,
		"aiman\x00pty\x00list\x00--id\x00s1\x00":                 false,
		"":                                                       false,
	} {
		if got := isHolderCmdline([]byte(cmdline)); got != want {
			t.Errorf("isHolderCmdline(%q) = %v, want %v", cmdline, got, want)
		}
	}
}

func TestHeldByHolder(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	mgr := ptyruntime.NewManagerWithRoot(shortTempDir(t), append([]string(nil), holderBin...))
	t.Cleanup(mgr.CloseAll)
	info, err := mgr.Create(ptyruntime.Spec{ID: "peer-holder", Command: "sleep 30", Dir: os.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !heldByHolder(info.PID) {
		t.Fatalf("session process %d not recognised as held", info.PID)
	}
	other := exec.Command("sleep", "30")
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Process.Kill(); _ = other.Wait() })
	if heldByHolder(other.Process.Pid) || heldByHolder(os.Getpid()) {
		t.Fatal("a process outside any holder counted as held")
	}
}
