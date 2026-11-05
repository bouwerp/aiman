package hoststats

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseProcStatHandlesNamesWithSpacesAndParens(t *testing.T) {
	line := "4242 (tmux: server (x)) S 100 4242 4242 0 -1 4194304 1 2 3 4 700 300 0 0 20 0 1 0 555 999 2048 18446744073709551615"
	got, ok := parseProcStat(line)
	if !ok {
		t.Fatal("not parsed")
	}
	if got.name != "tmux: server (x)" || got.ppid != 100 || got.ticks != 1000 || got.startTime != 555 || got.rssPages != 2048 {
		t.Fatalf("%+v", got)
	}
	if _, ok := parseProcStat("garbage"); ok {
		t.Fatal("garbage parsed")
	}
}

func TestParseCPUCountsIdleAndIOWait(t *testing.T) {
	total, idle := parseCPU("cpu  100 0 100 700 100 0 0 0 50 0\ncpu0 1 1 1 1\n")
	if total != 1000 || idle != 800 {
		t.Fatalf("total %d idle %d", total, idle)
	}
}

func TestParseMeminfo(t *testing.T) {
	mem, swap := parseMeminfo("MemTotal: 1000 kB\nMemFree: 1 kB\nMemAvailable: 400 kB\nSwapTotal: 100 kB\nSwapFree: 30 kB\n")
	if mem != (Memory{Total: 1024000, Used: 614400, Available: 409600}) {
		t.Fatalf("%+v", mem)
	}
	if swap != (Swap{Total: 102400, Used: 71680}) {
		t.Fatalf("%+v", swap)
	}
}

func TestDeltaComputesCPU(t *testing.T) {
	at := time.Now()
	prev := sample{at: at, cpuTotal: 1000, cpuIdle: 800, processes: []procStat{
		{pid: 1, ticks: 100, startTime: 5},
		{pid: 2, ticks: 900, startTime: 5},
	}}
	cur := sample{at: at.Add(time.Second), cpuTotal: 2000, cpuIdle: 1300, processes: []procStat{
		{pid: 1, ticks: 150, startTime: 5},
		{pid: 2, ticks: 10, startTime: 77}, // the PID was reused
		{pid: 3, ticks: 500, startTime: 9}, // seen once
	}}
	snap := delta(prev, cur)
	if snap.cpuPercent != 50 {
		t.Fatalf("host cpu %v", snap.cpuPercent)
	}
	want := map[int]float64{1: 50, 2: 0, 3: 0}
	for _, p := range snap.procs {
		if p.cpuPercent != want[p.pid] {
			t.Fatalf("pid %d cpu %v, want %v", p.pid, p.cpuPercent, want[p.pid])
		}
	}
}

func TestTopProcessesMergesCPUAndRSS(t *testing.T) {
	var procs []procUsage
	for i := 1; i <= 40; i++ {
		procs = append(procs, procUsage{procStat: procStat{pid: i, name: "p"}})
	}
	for i := range 15 {
		procs[i].cpuPercent = float64(100 - i) // pids 1-15 are busy
		procs[20+i].rssPages = uint64(100 - i) // pids 21-35 are big
	}
	got := NewAt("").topProcesses(procs)
	if len(got) != 30 {
		t.Fatalf("merged %d, want 30", len(got))
	}
	if got[0].PID != 1 {
		t.Fatalf("busiest first, got %+v", got[0])
	}
	seen := map[int]bool{}
	for _, p := range got {
		if seen[p.PID] {
			t.Fatalf("pid %d listed twice", p.PID)
		}
		seen[p.PID] = true
	}
	for _, pid := range []int{15, 35} {
		if !seen[pid] {
			t.Fatalf("pid %d missing", pid)
		}
	}
	if seen[16] {
		t.Fatal("pid 16 is in neither top list")
	}
	overlap := NewAt("").topProcesses([]procUsage{{procStat: procStat{pid: 1, rssPages: 1}, cpuPercent: 1}})
	if len(overlap) != 1 {
		t.Fatalf("a process in both lists counted twice: %+v", overlap)
	}
}

func TestSessionUsageWalksTheTree(t *testing.T) {
	procs := []procUsage{
		{procStat: procStat{pid: 10, ppid: 1, rssPages: 1}, cpuPercent: 5},
		{procStat: procStat{pid: 11, ppid: 10, rssPages: 2}, cpuPercent: 10},
		{procStat: procStat{pid: 12, ppid: 11, rssPages: 3}, cpuPercent: 20},
		{procStat: procStat{pid: 20, ppid: 1, rssPages: 100}, cpuPercent: 90},
	}
	got := sessionUsage(SessionRoot{Session: "s", Name: "n", PID: 10}, procs)
	want := SessionUsage{Session: "s", Name: "n", CPUPercent: 35, RSS: 6 * pageSize, Processes: 3}
	if got != want {
		t.Fatalf("%+v", got)
	}
	for _, pid := range []int{0, 999} {
		if got := sessionUsage(SessionRoot{Session: "s", PID: pid}, procs); got.Processes != 0 || got.RSS != 0 {
			t.Fatalf("pid %d: %+v", pid, got)
		}
	}
}

func TestRealMountsSkipsPseudoAndDuplicateDevices(t *testing.T) {
	filesystems := "nodev\tsysfs\nnodev\tproc\nnodev\ttmpfs\nnodev\toverlay\nnodev\tnfs4\nnodev\tzfs\n\text4\n\tsquashfs\n\tbtrfs\n"
	mounts := strings.Join([]string{
		"sysfs /sys sysfs rw 0 0",
		"proc /proc proc rw 0 0",
		"tmpfs /run tmpfs rw 0 0",
		"overlay /var/lib/docker/overlay2/x/merged overlay rw 0 0",
		"srv:/x /mnt/nfs nfs4 rw 0 0",
		"/dev/loop0 /snap/core squashfs ro 0 0",
		"/dev/sda1 / ext4 rw 0 0",
		"/dev/sda1 /var/lib/docker ext4 rw 0 0",
		`/dev/sdb1 /mnt/my\040disk ext4 rw 0 0`,
		"tank/data /tank zfs rw 0 0",
	}, "\n")
	got := realMounts(mounts, filesystems)
	want := []mount{{"/dev/sda1", "/"}, {"/dev/sdb1", "/mnt/my disk"}, {"tank/data", "/tank"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%+v", got)
	}
}

// fakeProc writes the /proc files the sampler reads.
func fakeProc(t *testing.T, ticks int) string {
	t.Helper()
	root := t.TempDir()
	put := func(rel, text string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	put("stat", fmt.Sprintf("cpu  %d 0 0 %d 0 0 0 0 0 0\n", ticks, ticks))
	put("uptime", "1234.5 100.0\n")
	put("loadavg", "0.5 1.5 2.5 1/100 42\n")
	put("meminfo", "MemTotal: 1000 kB\nMemAvailable: 400 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n")
	put("42/stat", fmt.Sprintf("42 (agent) S 1 42 42 0 -1 0 0 0 0 0 %d 0 0 0 20 0 1 0 7 9 10 18446744073709551615\n", ticks))
	put("42/cmdline", "agent\x00--token\x00SECRET-TOKEN\x00")
	return root
}

func TestReportFromAFakeProcTree(t *testing.T) {
	s := NewAt(fakeProc(t, 100))
	defer s.Close()
	rep := s.Report(context.Background(), []SessionRoot{{Session: "s1", Name: "one", PID: 42}, {Session: "s2", Name: "gone"}})
	if rep.UptimeSeconds != 1234 || rep.Load != [3]float64{0.5, 1.5, 2.5} {
		t.Fatalf("%+v", rep)
	}
	if rep.Memory.Total != 1024000 || rep.CPUCount != runtime.NumCPU() {
		t.Fatalf("%+v", rep)
	}
	if len(rep.Processes) != 1 || rep.Processes[0].Name != "agent" {
		t.Fatalf("%+v", rep.Processes)
	}
	if len(rep.Sessions) != 2 || rep.Sessions[0].Processes != 1 || rep.Sessions[1].Processes != 0 {
		t.Fatalf("%+v", rep.Sessions)
	}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "SECRET-TOKEN") {
		t.Fatalf("command-line argument leaked: %s", raw)
	}
}

func TestReportWithNoProcReturnsEmptyFieldsNotNull(t *testing.T) {
	s := NewAt(filepath.Join(t.TempDir(), "missing"))
	defer s.Close()
	raw, err := json.Marshal(s.Report(context.Background(), nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"disks":[]`, `"processes":[]`, `"sessions":[]`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("%s lacks %s", raw, want)
		}
	}
}

func TestOneSamplerServesEveryCaller(t *testing.T) {
	s := NewAt(fakeProc(t, 1))
	defer s.Close()
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { s.Report(context.Background(), nil) })
	}
	wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.starts != 1 {
		t.Fatalf("started %d samplers", s.starts)
	}
}

func TestSamplerStopsWhenIdleAndRestartsOnDemand(t *testing.T) {
	s := NewAt(fakeProc(t, 1))
	defer s.Close()
	s.firstDelta, s.sampleEvery, s.idleStop = time.Millisecond, time.Millisecond, time.Millisecond
	s.Report(context.Background(), nil)
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		running := s.running
		s.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sampler kept running with no callers")
		}
		time.Sleep(time.Millisecond)
	}
	s.idleStop = time.Minute
	if rep := s.Report(context.Background(), nil); rep.UptimeSeconds == 0 {
		t.Fatalf("no fresh sample after a restart: %+v", rep)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.starts != 2 {
		t.Fatalf("starts = %d", s.starts)
	}
}

func TestReportAfterFirstSampleIsFast(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	s := New()
	defer s.Close()
	s.Report(context.Background(), nil)
	start := time.Now()
	rep := s.Report(context.Background(), nil)
	if took := time.Since(start); took > 100*time.Millisecond {
		t.Fatalf("second report took %s", took)
	}
	if rep.Memory.Total == 0 || len(rep.Processes) == 0 {
		t.Fatalf("real /proc gave %+v", rep)
	}
	// Report does not wait for filesystems: they load on their own goroutine,
	// which can outlast the first sample on a busy host.
	for deadline := time.Now().Add(10 * time.Second); len(rep.Disks) == 0; rep = s.Report(context.Background(), nil) {
		if time.Now().After(deadline) {
			t.Fatalf("no filesystems after waiting: %+v", rep)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRealProcNeverCarriesArguments(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	const marker = "hoststats-secret-arg-9f3a"
	cmd := exec.Command("sh", "-c", "sleep 30 # "+marker)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	s := New()
	defer s.Close()
	rep := s.Report(context.Background(), []SessionRoot{{Session: "s", PID: cmd.Process.Pid}})
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), marker) {
		t.Fatal("command-line argument in the report")
	}
	if rep.Sessions[0].Processes == 0 {
		t.Fatalf("tree not found: %+v", rep.Sessions[0])
	}
}
