package hoststats

import (
	"cmp"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
)

// censusComm is the process name of the aiman binary. A comm is 15 bytes at
// most, so "aiman-gateway" and "aiman-trigger" never match it.
const censusComm = "aiman"

// censusParents is how many parents a census names.
const censusParents = 5

// AimanProcs counts the aiman processes on a host, read from /proc/<pid>/stat
// only (never cmdline or environ, whose arguments can hold secrets).
type AimanProcs struct {
	Total   int            `json:"total"`
	ByState map[string]int `json:"by_state"`
	Parents []ProcParent   `json:"parents"`
}

// ProcParent is a parent of counted processes.
type ProcParent struct {
	PID   int    `json:"pid"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// Census is CensusAt over /proc.
func Census(exclude map[int]bool) AimanProcs { return CensusAt("/proc", exclude) }

// CensusAt counts the processes under the /proc tree at proc whose comm is
// exactly "aiman", leaving out the PIDs in exclude.
func CensusAt(proc string, exclude map[int]bool) AimanProcs {
	out := AimanProcs{ByState: map[string]int{}, Parents: []ProcParent{}}
	entries, err := os.ReadDir(proc)
	if err != nil {
		return out
	}
	byParent := map[int]int{}
	names := map[int]string{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		text := readText(proc, e.Name(), "stat")
		name, state, ppid, ok := censusStat(text)
		if !ok {
			continue
		}
		names[pid] = name
		if name != censusComm || exclude[pid] {
			continue
		}
		out.Total++
		out.ByState[state]++
		byParent[ppid]++
	}
	for ppid, n := range byParent {
		name := names[ppid]
		if name == "" {
			name = strings.TrimSpace(readText(proc, strconv.Itoa(ppid), "comm"))
		}
		out.Parents = append(out.Parents, ProcParent{PID: ppid, Name: name, Count: n})
	}
	slices.SortFunc(out.Parents, func(a, b ProcParent) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.PID, b.PID))
	})
	if len(out.Parents) > censusParents {
		out.Parents = out.Parents[:censusParents]
	}
	return out
}

// censusStat reads comm, state and parent from /proc/<pid>/stat.
func censusStat(text string) (name, state string, ppid int, ok bool) {
	open, closeParen := strings.IndexByte(text, '('), strings.LastIndexByte(text, ')')
	if open < 0 || closeParen < open {
		return "", "", 0, false
	}
	f := strings.Fields(text[closeParen+1:])
	if len(f) < 2 {
		return "", "", 0, false
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return "", "", 0, false
	}
	return text[open+1 : closeParen], f[0], ppid, true
}

// Summary is the line serve logs after "procs: " and writes into a dump.
func (a AimanProcs) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d aiman processes outside holders, %d in D, parents: ", a.Total, a.ByState["D"])
	for i, p := range a.Parents {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s %d (%d)", p.Name, p.PID, p.Count)
	}
	return b.String()
}
