//go:build pileup_g

package hoststats

// Contract:
//   - type AimanProcs struct {
//         Total   int            `json:"total"`    // processes counted
//         ByState map[string]int `json:"by_state"` // keyed by the state letter of /proc/<pid>/stat ("R", "S", "D", ...)
//         Parents []ProcParent   `json:"parents"`  // top 5 by Count, then by PID ascending; never nil
//     }
//   - type ProcParent struct {
//         PID   int    `json:"pid"`
//         Name  string `json:"name"`  // the parent's comm from /proc/<ppid>/stat
//         Count int    `json:"count"` // counted processes whose parent this is
//     }
//   - func CensusAt(proc string, exclude map[int]bool) AimanProcs counts the
//     processes under the /proc tree at proc whose comm is exactly "aiman",
//     leaving out the PIDs in exclude (serve and the pty holders). It reads
//     /proc/<pid>/stat only: never cmdline, environ or any other per-process file.
//   - func Census(exclude map[int]bool) AimanProcs is CensusAt over /proc.
//   - func (a AimanProcs) Summary() string is the text serve logs after
//     "procs: " and writes into the dump:
//     "<total> aiman processes outside holders, <D count> in D, parents: <name> <pid> (<count>)[, <name> <pid> (<count>)...]"

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// censusProc builds a fake /proc whose per-process cmdline and environ are
// FIFOs with no writer: opening one for reading blocks, so a census that reads
// either never returns.
type censusProc struct {
	t    *testing.T
	root string
}

func newCensusProc(t *testing.T) *censusProc {
	t.Helper()
	return &censusProc{t: t, root: t.TempDir()}
}

func (c *censusProc) add(pid int, comm string, state byte, ppid int) {
	c.t.Helper()
	dir := filepath.Join(c.root, fmt.Sprint(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		c.t.Fatal(err)
	}
	stat := fmt.Sprintf("%d (%s) %c %d %d %d 0 -1 0 0 0 0 0 1 1 0 0 20 0 1 0 7 9 10 18446744073709551615\n", pid, comm, state, ppid, pid, pid)
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o600); err != nil {
		c.t.Fatal(err)
	}
	for _, name := range []string{"cmdline", "environ"} {
		if err := syscall.Mkfifo(filepath.Join(dir, name), 0o600); err != nil {
			c.t.Skipf("mkfifo: %v", err)
		}
	}
}

// census runs CensusAt and fails if it does not return, which is what reading
// a planted FIFO does.
func (c *censusProc) census(exclude map[int]bool) AimanProcs {
	c.t.Helper()
	done := make(chan AimanProcs, 1)
	go func() { done <- CensusAt(c.root, exclude) }()
	select {
	case got := <-done:
		return got
	case <-time.After(5 * time.Second):
		c.t.Fatal("CensusAt did not return: it opened /proc/<pid>/cmdline or environ")
		return AimanProcs{}
	}
}

// incident is a host where an ssh session piled up aiman CLIs, beside serve,
// two holders and processes that are not aiman.
func incident(t *testing.T) *censusProc {
	c := newCensusProc(t)
	c.add(1, "systemd", 'S', 0)
	c.add(100, "sshd-session", 'S', 1)
	c.add(200, "bash", 'S', 1)
	c.add(300, "claude", 'S', 1)
	c.add(400, "cron", 'S', 1)
	c.add(500, "node", 'S', 1)
	c.add(600, "make", 'S', 1)
	c.add(700, "zsh", 'S', 1)

	c.add(10, "aiman", 'S', 1)  // serve
	c.add(11, "aiman", 'S', 10) // holder
	c.add(12, "aiman", 'S', 10) // holder
	c.add(20, "aiman-gateway", 'S', 1)
	c.add(21, "aiman-trigger", 'D', 1)
	c.add(22, "aimanx", 'D', 100)

	pid := 1000
	spawn := func(parent, n int, state byte) {
		for range n {
			c.add(pid, "aiman", state, parent)
			pid++
		}
	}
	spawn(100, 4, 'D') // sshd-session: 6 in all
	spawn(100, 2, 'S')
	spawn(200, 2, 'R') // bash: 5
	spawn(200, 3, 'S')
	spawn(300, 4, 'S') // claude: 4
	spawn(400, 3, 'S') // cron: 3
	spawn(500, 1, 'D') // node: 2
	spawn(500, 1, 'S')
	spawn(600, 1, 'S') // make: 1, sixth parent, cut
	return c
}

func serveAndHolders() map[int]bool { return map[int]bool{10: true, 11: true, 12: true} }

func TestAcceptanceCensusCountsAimanByState(t *testing.T) {
	got := incident(t).census(serveAndHolders())
	if got.Total != 21 {
		t.Errorf("Total = %d, want 21 (comm exactly aiman, serve and holders left out)", got.Total)
	}
	want := map[string]int{"R": 2, "S": 14, "D": 5}
	for state, n := range want {
		if got.ByState[state] != n {
			t.Errorf("ByState[%s] = %d, want %d (all: %v)", state, got.ByState[state], n, got.ByState)
		}
	}
	sum := 0
	for _, n := range got.ByState {
		sum += n
	}
	if sum != got.Total {
		t.Errorf("ByState sums to %d, Total is %d", sum, got.Total)
	}
}

func TestAcceptanceCensusLeavesOutServeAndHolders(t *testing.T) {
	c := incident(t)
	all := c.census(nil)
	left := c.census(serveAndHolders())
	if all.Total != left.Total+3 {
		t.Fatalf("Total with no exclusions = %d, with serve and holders excluded = %d; want a difference of 3", all.Total, left.Total)
	}
	for _, p := range left.Parents {
		if p.PID == 10 {
			t.Errorf("serve's holders still counted under parent %+v", p)
		}
	}
}

func TestAcceptanceCensusTopFiveParents(t *testing.T) {
	got := incident(t).census(serveAndHolders())
	want := []ProcParent{
		{PID: 100, Name: "sshd-session", Count: 6},
		{PID: 200, Name: "bash", Count: 5},
		{PID: 300, Name: "claude", Count: 4},
		{PID: 400, Name: "cron", Count: 3},
		{PID: 500, Name: "node", Count: 2},
	}
	if fmt.Sprint(got.Parents) != fmt.Sprint(want) {
		t.Fatalf("Parents = %+v, want %+v", got.Parents, want)
	}
}

func TestAcceptanceCensusTiedParentsByPID(t *testing.T) {
	c := newCensusProc(t)
	c.add(1, "systemd", 'S', 0)
	c.add(90, "sshd-session", 'S', 1)
	c.add(80, "sshd-session", 'S', 1)
	c.add(1000, "aiman", 'D', 90)
	c.add(1001, "aiman", 'D', 80)
	got := c.census(nil)
	want := []ProcParent{{PID: 80, Name: "sshd-session", Count: 1}, {PID: 90, Name: "sshd-session", Count: 1}}
	if fmt.Sprint(got.Parents) != fmt.Sprint(want) {
		t.Fatalf("Parents = %+v, want %+v", got.Parents, want)
	}
}

func TestAcceptanceCensusSummary(t *testing.T) {
	got := incident(t).census(serveAndHolders()).Summary()
	want := "21 aiman processes outside holders, 5 in D, parents: sshd-session 100 (6), bash 200 (5), claude 300 (4), cron 400 (3), node 500 (2)"
	if got != want {
		t.Fatalf("Summary() =\n  %q\nwant\n  %q", got, want)
	}
}

func TestAcceptanceCensusEmptyMarshalsParentsAsList(t *testing.T) {
	c := newCensusProc(t)
	c.add(1, "systemd", 'S', 0)
	got := c.census(nil)
	if got.Total != 0 {
		t.Fatalf("Total = %d on a host with no aiman processes", got.Total)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"total":0`, `"by_state":`, `"parents":[]`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("%s lacks %s", raw, key)
		}
	}
}

func TestAcceptanceCensusNeverReadsCmdline(t *testing.T) {
	// Every process in the tree has a FIFO cmdline and environ; census fails
	// the test by timing out if it opens one.
	got := incident(t).census(serveAndHolders())
	if got.Total == 0 {
		t.Fatal("census counted nothing, so it proved nothing about cmdline")
	}
}
