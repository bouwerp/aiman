//go:build acceptance

package procwalk_test

import (
	"os"
	"testing"

	"github.com/bouwerp/aiman/internal/procwalk"
)

const holderCmdline = "aiman\x00pty\x00hold\x00--dir\x00/x\x00--id\x00s1"

// fakeProc is a process table: parents and command lines by PID. A PID
// missing from parents has an unreadable parent.
type fakeProc struct {
	parents  map[int]int
	cmdlines map[int]string
}

func (p fakeProc) parent(pid int) (int, bool) {
	pp, ok := p.parents[pid]
	return pp, ok
}

func (p fakeProc) cmdline(pid int) ([]byte, bool) {
	c, ok := p.cmdlines[pid]
	if !ok {
		return nil, false
	}
	return []byte(c), true
}

// chain is 300 → 200 → 100 → 1, with 100 running cmd100.
func chain(cmd100 string) fakeProc {
	return fakeProc{
		parents: map[int]int{300: 200, 200: 100, 100: 1, 1: 0},
		cmdlines: map[int]string{
			300: "bash\x00",
			200: "node\x00agent.js\x00",
			100: cmd100,
			1:   "/sbin/init\x00",
		},
	}
}

func TestInHolderTreeFindsHolderAncestor(t *testing.T) {
	p := chain(holderCmdline)
	for _, pid := range []int{300, 200, 100} {
		if !procwalk.InHolderTree(pid, p.parent, p.cmdline) {
			t.Errorf("InHolderTree(%d) = false, want true: 100 is a holder", pid)
		}
	}
}

func TestInHolderTreePlainBashIsNotAHolder(t *testing.T) {
	p := chain("bash\x00")
	for _, pid := range []int{300, 200, 100} {
		if procwalk.InHolderTree(pid, p.parent, p.cmdline) {
			t.Errorf("InHolderTree(%d) = true, want false: no holder in the chain", pid)
		}
	}
}

func TestInHolderTreeSelfParentLoopEndsFalse(t *testing.T) {
	p := fakeProc{
		parents:  map[int]int{300: 300},
		cmdlines: map[int]string{300: "bash\x00"},
	}
	if procwalk.InHolderTree(300, p.parent, p.cmdline) {
		t.Fatal("InHolderTree on a self-parent loop = true, want false")
	}
}

func TestInHolderTreeStopsAtUnreadableParent(t *testing.T) {
	// 300's parent is 200, whose parent cannot be read; the holder 100 above
	// it is never reached.
	p := chain(holderCmdline)
	delete(p.parents, 200)
	if procwalk.InHolderTree(300, p.parent, p.cmdline) {
		t.Fatal("InHolderTree walked past an unreadable parent")
	}
}

func TestIsHolderCmdline(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"holder", holderCmdline, true},
		{"renamed binary", "/opt/bin/aiman-v0.33\x00pty\x00hold\x00--dir\x00/x\x00--id\x00s1\x00", true},
		{"pty list", "aiman\x00pty\x00list\x00", false},
		{"hold without --id", "aiman\x00pty\x00hold\x00--dir\x00/x\x00", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := procwalk.IsHolderCmdline([]byte(c.raw)); got != c.want {
				t.Fatalf("IsHolderCmdline(%q) = %v, want %v", c.raw, got, c.want)
			}
		})
	}
}

// Self is the walk from this process with the real readers. A test binary is
// not normally run under a holder, so it is false here; the check pins the
// signature and that it does not panic on a real /proc.
func TestSelfOutsideAHolder(t *testing.T) {
	var self func() bool = procwalk.Self
	var _ func(int) (int, bool) = procwalk.ParentPID
	var _ func(int) ([]byte, bool) = procwalk.Cmdline
	if os.Getenv("AIMAN_ID") != "" {
		t.Skip("running inside an aiman session: Self is expected to be true here")
	}
	if self() {
		t.Fatal("Self() = true for a test process outside any holder")
	}
}
