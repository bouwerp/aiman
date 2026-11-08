package procwalk

import (
	"os"
	"runtime"
	"testing"
)

func TestParentPIDAndCmdlineReadThisProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	pp, ok := ParentPID(os.Getpid())
	if !ok || pp != os.Getppid() {
		t.Fatalf("ParentPID(self) = %d, %v; want %d, true", pp, ok, os.Getppid())
	}
	raw, ok := Cmdline(os.Getpid())
	if !ok || len(raw) == 0 {
		t.Fatalf("Cmdline(self) = %q, %v; want a command line", raw, ok)
	}
	if _, ok := ParentPID(-1); ok {
		t.Fatal("ParentPID(-1) read a parent")
	}
}
