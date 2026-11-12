package cgroup

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParsePressure(t *testing.T) {
	raw := "some avg10=61.25 avg60=0.18 avg300=1.81 total=8838764221\nfull avg10=0.46 avg60=0.12 avg300=1.59 total=7497116825\n"
	if got := parsePressure(raw); got != 61.25 {
		t.Fatalf("parsePressure = %v", got)
	}
	for _, bad := range []string{"", "full avg10=3.00\n", "some avg60=1.00\n", "some avg10=x\n"} {
		if got := parsePressure(bad); got != 0 {
			t.Errorf("parsePressure(%q) = %v", bad, got)
		}
	}
}

func TestParseKeyedAndValue(t *testing.T) {
	got := parseKeyed(bufio.NewScanner(strings.NewReader("usage_usec 881166033605\nuser_usec 5\nbroken\nname x\n")))
	if got["usage_usec"] != 881166033605 || got["user_usec"] != 5 || len(got) != 2 {
		t.Fatalf("parseKeyed = %v", got)
	}
	if v, ok := parseValue("max\n"); !ok || v != Infinity {
		t.Fatalf("max = %d, %v", v, ok)
	}
	if v, ok := parseValue("25769803776\n"); !ok || v != 25769803776 {
		t.Fatalf("number = %d, %v", v, ok)
	}
	if _, ok := parseValue("junk"); ok {
		t.Fatal("junk parsed")
	}
}

// writeCgroup writes the files of one fake cgroup directory.
func writeCgroup(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSamplerReadsScopesAndCPUDelta(t *testing.T) {
	base := t.TempDir()
	sd := NewSystemd(filepath.Join(base, "user-1.slice", "user@1.service"), nil)
	proc := filepath.Join(base, "pressure")
	writeCgroup(t, proc, map[string]string{
		"memory": "some avg10=12.50 avg60=0 avg300=0 total=1\n",
		"cpu":    "some avg10=3.00 avg60=0 avg300=0 total=1\n",
		"io":     "some avg10=0.25 avg60=0 avg300=0 total=1\n",
	})
	writeCgroup(t, sd.UserSliceDir(), map[string]string{"memory.current": "100\n", "memory.high": "25769803776\n", "memory.max": "max\n"})
	writeCgroup(t, sd.SliceDir(), map[string]string{"memory.current": "50\n", "memory.high": "21474836480\n"})
	scope := sd.ScopeDir("s1")
	writeCgroup(t, scope, map[string]string{
		"memory.current":  "5368709120\n",
		"memory.high":     "6442450944\n",
		"memory.max":      "10737418240\n",
		"memory.stat":     "anon 4294967296\nfile 10\nshmem 1073741824\n",
		"memory.pressure": "some avg10=61.25 avg60=0 avg300=0 total=1\nfull avg10=1.00 avg60=0 avg300=0 total=1\n",
		"memory.events":   "low 0\nhigh 12\nmax 0\noom 1\noom_kill 2\n",
		"cpu.stat":        "usage_usec 1000000\nuser_usec 1\n",
		"pids.current":    "42\n",
	})

	s := NewSampler(sd)
	s.procPressure = proc
	s.Sessions = func(context.Context) []SessionRef {
		return []SessionRef{{ID: "s1", Name: "fix-login"}, {ID: "unscoped", Name: "old"}}
	}
	t0 := time.Unix(1000, 0)
	first := s.Sample(context.Background(), t0)
	if !first.Supported || len(first.Sessions) != 1 {
		t.Fatalf("first sample: %+v", first)
	}
	got := first.Sessions[0]
	want := Usage{
		MemoryCurrent: 5 << 30, MemoryHigh: 6 << 30, MemoryMax: 10 << 30,
		Anon: 4 << 30, Shmem: 1 << 30, MemoryPressure: 61.25, Pids: 42, OOMKills: 2,
	}
	if got.ID != "s1" || got.Name != "fix-login" || got.Usage != want {
		t.Fatalf("session usage = %+v\nwant %+v", got, want)
	}
	if first.Host != (HostPressure{Memory: 12.5, CPU: 3, IO: 0.25}) {
		t.Fatalf("host = %+v", first.Host)
	}
	if first.UserSlice == nil || first.UserSlice.MemoryHigh != 24<<30 || first.UserSlice.MemoryMax != Infinity {
		t.Fatalf("user slice = %+v", first.UserSlice)
	}
	if first.SessionsSlice == nil || first.SessionsSlice.MemoryHigh != 20<<30 {
		t.Fatalf("sessions slice = %+v", first.SessionsSlice)
	}

	// 15 CPU-seconds over a 10 second interval is 150% of one core.
	writeCgroup(t, scope, map[string]string{"cpu.stat": "usage_usec 16000000\n"})
	second := s.Sample(context.Background(), t0.Add(10*time.Second))
	if cpu := second.Sessions[0].CPUPercent; cpu < 149.9 || cpu > 150.1 {
		t.Fatalf("cpu percent = %v, want 150", cpu)
	}
	if s.Snapshot().At != second.At {
		t.Fatal("Snapshot is not the last sample")
	}

	// A scope that is gone drops out, with its CPU baseline.
	if err := os.RemoveAll(scope); err != nil {
		t.Fatal(err)
	}
	third := s.Sample(context.Background(), t0.Add(20*time.Second))
	if len(third.Sessions) != 0 {
		t.Fatalf("gone scope still sampled: %+v", third.Sessions)
	}
	if _, kept := s.cpu[scope]; kept {
		t.Fatal("gone scope kept its CPU baseline")
	}
}

func TestSamplerUnsupported(t *testing.T) {
	var none *Sampler
	if snap := none.Snapshot(); snap.Supported || snap.Sessions == nil {
		t.Fatalf("nil sampler: %+v", snap)
	}
	s := NewSampler(nil)
	if snap := s.Sample(context.Background(), time.Now()); snap.Supported || snap.Sessions == nil {
		t.Fatalf("no systemd: %+v", snap)
	}
	s.Run(context.Background()) // returns at once
}
