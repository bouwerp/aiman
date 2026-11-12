package cgroup

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SampleInterval is how often a Sampler reads the cgroup files.
const SampleInterval = 10 * time.Second

// Usage is one cgroup's resource use. Sizes are bytes; a limit of -1 is
// "no limit" and 0 is "not read".
type Usage struct {
	MemoryCurrent int64 `json:"memory_current"`
	MemoryHigh    int64 `json:"memory_high,omitempty"`
	MemoryMax     int64 `json:"memory_max,omitempty"`
	// Anon and Shmem split MemoryCurrent: process memory, and tmpfs files
	// plus shared memory, which stay charged until they are deleted.
	Anon  int64 `json:"anon"`
	Shmem int64 `json:"shmem"`
	// MemoryPressure is the share of the last 10 seconds in which some
	// task of the cgroup stalled on memory, in percent.
	MemoryPressure float64 `json:"memory_pressure"`
	// CPUPercent is CPU time over the last interval, in percent of one
	// core. It is 0 on a cgroup's first sample.
	CPUPercent float64 `json:"cpu_percent"`
	Pids       int64   `json:"pids"`
	// OOMKills counts processes the kernel killed at MemoryMax.
	OOMKills int64 `json:"oom_kills,omitempty"`
}

// SessionUsage is one session's scope.
type SessionUsage struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Usage
}

// HostPressure is /proc/pressure: the share of the last 10 seconds in which
// some task on the host stalled on each resource, in percent.
type HostPressure struct {
	Memory float64 `json:"memory"`
	CPU    float64 `json:"cpu"`
	IO     float64 `json:"io"`
}

// Snapshot is one reading of every session scope and the totals above them.
type Snapshot struct {
	// Supported is false on a host without session scopes; the other
	// fields are then empty.
	Supported bool         `json:"supported"`
	At        time.Time    `json:"at,omitzero"`
	Host      HostPressure `json:"host"`
	// UserSlice is the whole user: every session, serve and the gateway.
	UserSlice *Usage `json:"user_slice,omitempty"`
	// SessionsSlice is every session scope together.
	SessionsSlice *Usage         `json:"sessions_slice,omitempty"`
	Sessions      []SessionUsage `json:"sessions"`
}

// SessionRef names a session the sampler should look for.
type SessionRef struct {
	ID, Name string
}

// Sampler reads the session scopes on a timer and keeps the last snapshot.
type Sampler struct {
	sd *Systemd
	// Sessions lists the sessions to sample. A session without a scope
	// (started before scopes, or after a failed launch) is left out.
	Sessions func(ctx context.Context) []SessionRef
	// OnSample runs after each pass, on the sampler's goroutine.
	OnSample func(Snapshot)
	// procPressure is /proc/pressure; tests point it elsewhere.
	procPressure string

	mu   sync.Mutex
	last Snapshot
	cpu  map[string]cpuPoint
}

type cpuPoint struct {
	usec int64
	at   time.Time
}

// NewSampler returns a sampler over sd. A nil sd gives a sampler whose
// snapshot is always unsupported.
func NewSampler(sd *Systemd) *Sampler {
	return &Sampler{sd: sd, procPressure: "/proc/pressure", cpu: map[string]cpuPoint{}}
}

// Snapshot returns the last reading.
func (s *Sampler) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{Sessions: []SessionUsage{}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last.Sessions == nil {
		return Snapshot{Supported: s.sd != nil, Sessions: []SessionUsage{}}
	}
	return s.last
}

// Run samples now and then every SampleInterval until ctx ends.
func (s *Sampler) Run(ctx context.Context) {
	if s == nil || s.sd == nil {
		return
	}
	tick := time.NewTicker(SampleInterval)
	defer tick.Stop()
	for {
		snap := s.Sample(ctx, time.Now())
		if s.OnSample != nil {
			s.OnSample(snap)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Sample reads everything once and stores the result.
func (s *Sampler) Sample(ctx context.Context, now time.Time) Snapshot {
	if s.sd == nil {
		return s.Snapshot()
	}
	snap := Snapshot{Supported: true, At: now.UTC(), Sessions: []SessionUsage{}}
	snap.Host = HostPressure{
		Memory: readPressure(filepath.Join(s.procPressure, "memory")),
		CPU:    readPressure(filepath.Join(s.procPressure, "cpu")),
		IO:     readPressure(filepath.Join(s.procPressure, "io")),
	}
	seen := map[string]bool{}
	read := func(dir string) (Usage, bool) {
		u, usec, ok := readUsage(dir)
		if !ok {
			return Usage{}, false
		}
		seen[dir] = true
		s.mu.Lock()
		if prev, had := s.cpu[dir]; had && now.After(prev.at) && usec >= prev.usec {
			u.CPUPercent = float64(usec-prev.usec) / float64(now.Sub(prev.at).Microseconds()) * 100
		}
		s.cpu[dir] = cpuPoint{usec: usec, at: now}
		s.mu.Unlock()
		return u, true
	}
	if u, ok := read(s.sd.UserSliceDir()); ok {
		snap.UserSlice = &u
	}
	if u, ok := read(s.sd.SliceDir()); ok {
		snap.SessionsSlice = &u
	}
	if s.Sessions != nil {
		for _, ref := range s.Sessions(ctx) {
			if u, ok := read(s.sd.ScopeDir(ref.ID)); ok {
				snap.Sessions = append(snap.Sessions, SessionUsage{ID: ref.ID, Name: ref.Name, Usage: u})
			}
		}
	}
	s.mu.Lock()
	for dir := range s.cpu {
		if !seen[dir] {
			delete(s.cpu, dir)
		}
	}
	s.last = snap
	s.mu.Unlock()
	return snap
}

// readUsage reads one cgroup directory. ok is false when the cgroup does
// not exist. usec is the cgroup's total CPU time.
func readUsage(dir string) (u Usage, usec int64, ok bool) {
	cur, ok := readValue(dir, "memory.current")
	if !ok {
		return Usage{}, 0, false
	}
	u.MemoryCurrent = cur
	u.MemoryHigh, _ = readValue(dir, "memory.high")
	u.MemoryMax, _ = readValue(dir, "memory.max")
	u.Pids, _ = readValue(dir, "pids.current")
	stat := readKeyed(filepath.Join(dir, "memory.stat"))
	u.Anon, u.Shmem = stat["anon"], stat["shmem"]
	u.OOMKills = readKeyed(filepath.Join(dir, "memory.events"))["oom_kill"]
	u.MemoryPressure = readPressure(filepath.Join(dir, "memory.pressure"))
	return u, readKeyed(filepath.Join(dir, "cpu.stat"))["usage_usec"], true
}

// readKeyed reads a "key value" file such as memory.stat or cpu.stat.
func readKeyed(path string) map[string]int64 {
	f, err := os.Open(path) //nolint:gosec // G304: a cgroup control file
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseKeyed(bufio.NewScanner(f))
}

func parseKeyed(sc *bufio.Scanner) map[string]int64 {
	out := map[string]int64{}
	for sc.Scan() {
		key, val, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if !ok {
			continue
		}
		if v, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64); err == nil {
			out[key] = v
		}
	}
	return out
}

func readPressure(path string) float64 {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: a pressure file under /proc or a cgroup
	if err != nil {
		return 0
	}
	return parsePressure(string(raw))
}

// parsePressure returns avg10 of the "some" line of a pressure file:
//
//	some avg10=0.70 avg60=0.18 avg300=1.81 total=8838764221
func parsePressure(raw string) float64 {
	for _, line := range strings.Split(raw, "\n") {
		rest, ok := strings.CutPrefix(line, "some ")
		if !ok {
			continue
		}
		for _, field := range strings.Fields(rest) {
			if v, ok := strings.CutPrefix(field, "avg10="); ok {
				f, _ := strconv.ParseFloat(v, 64)
				return f
			}
		}
	}
	return 0
}
