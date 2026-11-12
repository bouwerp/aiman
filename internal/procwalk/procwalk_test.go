package procwalk

import (
	"os"
	"runtime"
	"slices"
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

func TestDescendants(t *testing.T) {
	parents := map[int]int{10: 1, 11: 10, 12: 11, 20: 1, 21: 20, 30: 12}
	got := Descendants([]int{10, 99, 10}, parents)
	if want := []int{10, 11, 12, 30}; !slices.Equal(got, want) {
		t.Fatalf("Descendants = %v, want %v", got, want)
	}
	if got := Descendants(nil, parents); len(got) != 0 {
		t.Fatalf("no roots: %v", got)
	}
}

func TestParentsAndEnvValueReadThisProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs /proc")
	}
	if pp, ok := Parents()[os.Getpid()]; !ok || pp != os.Getppid() {
		t.Fatalf("Parents()[self] = %d, %v; want %d", pp, ok, os.Getppid())
	}
	if _, ok := EnvValue(os.Getpid(), "AIMAN_NO_SUCH_VARIABLE"); ok {
		t.Fatal("found a variable that is not set")
	}
	if v, ok := EnvValue(os.Getpid(), "PATH"); !ok || v == "" {
		t.Fatalf("PATH = %q, %v", v, ok)
	}
}
