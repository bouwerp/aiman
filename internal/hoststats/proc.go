package hoststats

import (
	"cmp"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// clkTck is USER_HZ, the unit of /proc CPU times. Linux fixes it at 100 on
// every architecture aiman ships for.
const clkTck = 100

const pageSize = 4096

// procStat is one line of /proc/<pid>/stat.
type procStat struct {
	pid       int
	ppid      int
	name      string
	ticks     uint64
	rssPages  uint64
	startTime uint64
	uid       uint32
	uidKnown  bool
}

// procUsage is a process with its CPU over the last delta.
type procUsage struct {
	procStat
	cpuPercent float64
}

// sample is one read of /proc.
type sample struct {
	at        time.Time
	cpuTotal  uint64
	cpuIdle   uint64
	uptime    int64
	load      [3]float64
	memory    Memory
	swap      Swap
	processes []procStat
}

func readSample(proc string) sample {
	s := sample{at: time.Now()}
	s.cpuTotal, s.cpuIdle = parseCPU(readText(proc, "stat"))
	s.uptime = parseUptime(readText(proc, "uptime"))
	s.load = parseLoad(readLoadAvgText(proc))
	s.memory, s.swap = parseMeminfo(readText(proc, "meminfo"))
	entries, err := os.ReadDir(proc)
	if err != nil {
		return s
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		st, ok := parseProcStat(readText(proc, e.Name(), "stat"))
		if !ok {
			continue
		}
		st.pid = pid
		if info, err := e.Info(); err == nil {
			st.uid, st.uidKnown = fileUID(info)
		}
		s.processes = append(s.processes, st)
	}
	return s
}

func readText(parts ...string) string {
	raw, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		return ""
	}
	return string(raw)
}

// delta turns two samples into a snapshot. A PID whose start time changed is a
// new process, and a process seen once has no CPU figure yet.
func delta(prev, cur sample) *snapshot {
	snap := &snapshot{uptime: cur.uptime, load: cur.load, memory: cur.memory, swap: cur.swap}
	if total := cur.cpuTotal - prev.cpuTotal; cur.cpuTotal > prev.cpuTotal {
		idle := cur.cpuIdle - prev.cpuIdle
		snap.cpuPercent = percent(float64(total-min(idle, total)), float64(total))
	}
	elapsed := cur.at.Sub(prev.at).Seconds()
	before := make(map[int]procStat, len(prev.processes))
	for _, p := range prev.processes {
		before[p.pid] = p
	}
	snap.procs = make([]procUsage, 0, len(cur.processes))
	for _, p := range cur.processes {
		u := procUsage{procStat: p}
		if old, ok := before[p.pid]; ok && old.startTime == p.startTime && elapsed > 0 && p.ticks >= old.ticks {
			u.cpuPercent = roundPercent(float64(p.ticks-old.ticks) / clkTck / elapsed * 100)
		}
		snap.procs = append(snap.procs, u)
	}
	return snap
}

func percent(part, whole float64) float64 {
	if whole <= 0 {
		return 0
	}
	return roundPercent(part / whole * 100)
}

func roundPercent(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}

// topProcesses merges the top processes by CPU and by RSS, busiest first.
func (s *Sampler) topProcesses(procs []procUsage) []Process {
	byCPU := slices.Clone(procs)
	slices.SortStableFunc(byCPU, func(a, b procUsage) int {
		return cmp.Or(cmp.Compare(b.cpuPercent, a.cpuPercent), cmp.Compare(b.rssPages, a.rssPages), cmp.Compare(a.pid, b.pid))
	})
	byRSS := slices.Clone(procs)
	slices.SortStableFunc(byRSS, func(a, b procUsage) int {
		return cmp.Or(cmp.Compare(b.rssPages, a.rssPages), cmp.Compare(a.pid, b.pid))
	})
	seen := map[int]bool{}
	merged := []procUsage{}
	for _, list := range [][]procUsage{byCPU, byRSS} {
		for _, p := range list[:min(topProcesses, len(list))] {
			if !seen[p.pid] {
				seen[p.pid] = true
				merged = append(merged, p)
			}
		}
	}
	slices.SortStableFunc(merged, func(a, b procUsage) int {
		return cmp.Or(cmp.Compare(b.cpuPercent, a.cpuPercent), cmp.Compare(b.rssPages, a.rssPages), cmp.Compare(a.pid, b.pid))
	})
	out := make([]Process, 0, len(merged))
	for _, p := range merged {
		out = append(out, Process{PID: p.pid, User: s.userName(p.procStat), Name: p.name, CPUPercent: p.cpuPercent, RSS: p.rssPages * pageSize})
	}
	return out
}

// userName caches lookups: they read /etc/passwd or call out to NSS.
func (s *Sampler) userName(p procStat) string {
	if !p.uidKnown {
		return ""
	}
	s.mu.Lock()
	name, ok := s.users[p.uid]
	s.mu.Unlock()
	if ok {
		return name
	}
	id := strconv.FormatUint(uint64(p.uid), 10)
	name = id
	if u, err := user.LookupId(id); err == nil {
		name = u.Username
	}
	s.mu.Lock()
	s.users[p.uid] = name
	s.mu.Unlock()
	return name
}

// sessionUsage totals the process tree under root.PID.
func sessionUsage(root SessionRoot, procs []procUsage) SessionUsage {
	out := SessionUsage{Session: root.Session, Name: root.Name}
	if root.PID <= 0 {
		return out
	}
	children := make(map[int][]int, len(procs))
	byPID := make(map[int]procUsage, len(procs))
	for _, p := range procs {
		children[p.ppid] = append(children[p.ppid], p.pid)
		byPID[p.pid] = p
	}
	if _, ok := byPID[root.PID]; !ok {
		return out
	}
	seen := map[int]bool{}
	stack := []int{root.PID}
	for len(stack) > 0 {
		pid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		p := byPID[pid]
		out.Processes++
		out.CPUPercent += p.cpuPercent
		out.RSS += p.rssPages * pageSize
		stack = append(stack, children[pid]...)
	}
	out.CPUPercent = roundPercent(out.CPUPercent)
	return out
}

// parseProcStat reads /proc/<pid>/stat. The name sits in parentheses and may
// itself hold spaces and parentheses, so the fields after it are counted from
// the last ')'.
func parseProcStat(text string) (procStat, bool) {
	open, closeParen := strings.IndexByte(text, '('), strings.LastIndexByte(text, ')')
	if open < 0 || closeParen < open {
		return procStat{}, false
	}
	// Index 0 here is field 3 (state) of proc(5).
	f := strings.Fields(text[closeParen+1:])
	if len(f) < 22 {
		return procStat{}, false
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return procStat{}, false
	}
	return procStat{
		name:      text[open+1 : closeParen],
		ppid:      ppid,
		ticks:     parseUint(f[11]) + parseUint(f[12]),
		startTime: parseUint(f[19]),
		rssPages:  parseUint(f[21]),
	}, true
}

// parseCPU reads the aggregate line of /proc/stat: the total of all its
// columns, and the part of that spent idle or waiting for I/O.
func parseCPU(text string) (total, idle uint64) {
	line, _, _ := strings.Cut(text, "\n")
	f := strings.Fields(line)
	if len(f) < 6 || f[0] != "cpu" {
		return 0, 0
	}
	// guest and guest_nice are already inside user and nice.
	for _, v := range f[1:min(len(f), 9)] {
		total += parseUint(v)
	}
	return total, parseUint(f[4]) + parseUint(f[5])
}

func parseUptime(text string) int64 {
	f := strings.Fields(text)
	if len(f) == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0
	}
	return int64(v)
}

func parseLoad(text string) [3]float64 {
	var load [3]float64
	f := strings.Fields(text)
	for i := 0; i < 3 && i < len(f); i++ {
		if v, err := strconv.ParseFloat(f[i], 64); err == nil {
			load[i] = v
		}
	}
	return load
}

func parseMeminfo(text string) (Memory, Swap) {
	vals := map[string]uint64{}
	for line := range strings.SplitSeq(text, "\n") {
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if f := strings.Fields(rest); len(f) > 0 {
			vals[key] = parseUint(f[0]) * 1024
		}
	}
	mem := Memory{Total: vals["MemTotal"], Available: vals["MemAvailable"]}
	if mem.Total > mem.Available {
		mem.Used = mem.Total - mem.Available
	}
	swap := Swap{Total: vals["SwapTotal"]}
	if swap.Total > vals["SwapFree"] {
		swap.Used = swap.Total - vals["SwapFree"]
	}
	return mem, swap
}

func parseUint(s string) uint64 {
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// mount is one /proc/mounts line that names a real filesystem.
type mount struct{ device, path string }

// skippedFSTypes hold real devices but no usable space: read-only images.
var skippedFSTypes = map[string]bool{"squashfs": true, "iso9660": true}

// nodevAllowed are real filesystems /proc/filesystems marks nodev.
var nodevAllowed = map[string]bool{"zfs": true}

// realMounts keeps filesystems with a backing device, one mount per device (a
// bind mount or btrfs subvolume repeats its device), the shortest path winning.
// /proc/filesystems marks every pseudo filesystem nodev; network filesystems
// are nodev too, and a dead one would block statfs.
func realMounts(mounts, filesystems string) []mount {
	nodev := map[string]bool{}
	for line := range strings.SplitSeq(filesystems, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "nodev" {
			nodev[f[1]] = true
		}
	}
	best := map[string]string{}
	var order []string
	for line := range strings.SplitSeq(mounts, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		device, where, fstype := f[0], unescapeMount(f[1]), f[2]
		if skippedFSTypes[fstype] || (nodev[fstype] && !nodevAllowed[fstype]) {
			continue
		}
		cur, ok := best[device]
		if !ok {
			order = append(order, device)
		}
		if !ok || len(where) < len(cur) {
			best[device] = where
		}
	}
	out := make([]mount, 0, len(order))
	for _, d := range order {
		out = append(out, mount{device: d, path: best[d]})
	}
	slices.SortFunc(out, func(a, b mount) int { return cmp.Compare(a.path, b.path) })
	return out
}

// unescapeMount undoes the octal escapes /proc/mounts uses for spaces and the like.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func readDisks(proc string) []Disk {
	disks := []Disk{}
	for _, m := range realMounts(readText(proc, "mounts"), readText(proc, "filesystems")) {
		total, used, ok := diskSpace(m.path)
		if ok && total > 0 {
			disks = append(disks, Disk{Mount: m.path, Total: total, Used: used})
		}
	}
	return disks
}
