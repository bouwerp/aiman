// Package hoststats reports what a host is doing, from /proc. It never reads a
// process's command line, which can hold secrets: a process is known by the
// name in /proc/<pid>/stat only.
package hoststats

import (
	"context"
	"os"
	"runtime"
	"sync"
	"time"
)

const (
	defaultFirstDelta  = 200 * time.Millisecond
	defaultSampleEvery = 2 * time.Second
	defaultIdleStop    = time.Minute
	diskEvery          = 30 * time.Second
	// firstReportWait bounds how long the first caller waits for a sample.
	firstReportWait = 2 * time.Second
	topProcesses    = 15
)

// Memory is physical memory in bytes.
type Memory struct {
	Total     uint64 `json:"total"`
	Used      uint64 `json:"used"`
	Available uint64 `json:"available"`
}

// Swap is swap space in bytes.
type Swap struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

// Disk is one real filesystem, in bytes.
type Disk struct {
	Mount string `json:"mount"`
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

// Process is one process. CPUPercent is of one core, so a busy multi-threaded
// process can pass 100. There is deliberately no command-line field.
type Process struct {
	PID        int     `json:"pid"`
	User       string  `json:"user"`
	Name       string  `json:"name"`
	CPUPercent float64 `json:"cpu_percent"`
	RSS        uint64  `json:"rss"`
}

// SessionUsage is the process tree of one session's agent.
type SessionUsage struct {
	Session    string  `json:"session"`
	Name       string  `json:"name"`
	CPUPercent float64 `json:"cpu_percent"`
	RSS        uint64  `json:"rss"`
	Processes  int     `json:"processes"`
}

// SessionRoot names a live session and the PID its tree hangs off. PID 0 is a
// session whose process could not be confirmed; it reports zero usage.
type SessionRoot struct {
	Session string
	Name    string
	PID     int
}

// Report is one host.stats answer. CPUPercent is of the whole host, 0 to 100.
// Slices are never nil, so they marshal as [].
type Report struct {
	Host          string         `json:"host"`
	UptimeSeconds int64          `json:"uptime_seconds"`
	Load          [3]float64     `json:"load"`
	CPUCount      int            `json:"cpu_count"`
	CPUPercent    float64        `json:"cpu_percent"`
	Memory        Memory         `json:"memory"`
	Swap          Swap           `json:"swap"`
	Disks         []Disk         `json:"disks"`
	Processes     []Process      `json:"processes"`
	Sessions      []SessionUsage `json:"sessions"`
}

// snapshot is everything the last sample learned.
type snapshot struct {
	uptime     int64
	load       [3]float64
	cpuPercent float64
	memory     Memory
	swap       Swap
	procs      []procUsage
}

// Sampler keeps the previous /proc sample, because CPU percent needs a delta.
// One goroutine samples while callers keep asking. It starts on the first
// Report and stops itself after a quiet minute, so an idle host pays nothing.
// Reports read the latest snapshot, so a slow /proc cannot hold a caller up.
type Sampler struct {
	proc        string
	firstDelta  time.Duration
	sampleEvery time.Duration
	idleStop    time.Duration

	mu        sync.Mutex
	running   bool
	stop      chan struct{}
	lastCall  time.Time
	snap      *snapshot
	ready     chan struct{}
	disks     []Disk
	disksBusy bool
	disksAt   time.Time
	users     map[uint32]string
	starts    int
}

// New returns a Sampler over /proc. It does no work until the first Report.
func New() *Sampler { return NewAt(procRoot) }

// NewAt returns a Sampler over a /proc tree at root.
func NewAt(root string) *Sampler {
	return &Sampler{
		proc:        root,
		firstDelta:  defaultFirstDelta,
		sampleEvery: defaultSampleEvery,
		idleStop:    defaultIdleStop,
		users:       map[uint32]string{},
	}
}

// Report returns the latest stats. The first call after a quiet period waits
// for one short sample; the others return at once.
func (s *Sampler) Report(ctx context.Context, sessions []SessionRoot) Report {
	ready := s.touch()
	timer := time.NewTimer(firstReportWait)
	defer timer.Stop()
	select {
	case <-ready:
	case <-ctx.Done():
	case <-timer.C:
	}

	s.mu.Lock()
	snap, disks := s.snap, append([]Disk{}, s.disks...)
	s.mu.Unlock()

	rep := Report{CPUCount: runtime.NumCPU(), Disks: disks, Processes: []Process{}, Sessions: []SessionUsage{}}
	if host, err := os.Hostname(); err == nil {
		rep.Host = host
	}
	if snap != nil {
		rep.UptimeSeconds, rep.Load, rep.CPUPercent = snap.uptime, snap.load, snap.cpuPercent
		rep.Memory, rep.Swap = snap.memory, snap.swap
		rep.Processes = s.topProcesses(snap.procs)
	}
	for _, root := range sessions {
		var procs []procUsage
		if snap != nil {
			procs = snap.procs
		}
		rep.Sessions = append(rep.Sessions, sessionUsage(root, procs))
	}
	return rep
}

// Close stops the sampling goroutine, if any. A later Report starts a new one.
func (s *Sampler) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		close(s.stop)
		s.running = false
		s.snap = nil
	}
}

// touch records a call, starts the sampler if none is running, and returns the
// channel that closes once a snapshot exists.
func (s *Sampler) touch() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastCall = time.Now()
	if !s.running {
		s.running = true
		s.stop = make(chan struct{})
		s.ready = make(chan struct{})
		s.starts++
		go s.loop(s.stop, s.ready)
	}
	return s.ready
}

func (s *Sampler) loop(stop <-chan struct{}, ready chan<- struct{}) {
	s.refreshDisks()
	prev := readSample(s.proc)
	if !sleep(stop, s.firstDelta) {
		return
	}
	first := true
	for {
		cur := readSample(s.proc)
		if !s.publish(stop, delta(prev, cur)) {
			return
		}
		prev = cur
		if first {
			close(ready)
			first = false
		}
		if !sleep(stop, s.sampleEvery) || !s.keepRunning(stop) {
			return
		}
		s.refreshDisks()
	}
}

// publish stores snap unless Close ended this loop while it sampled.
func (s *Sampler) publish(stop <-chan struct{}, snap *snapshot) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-stop:
		return false
	default:
	}
	s.snap = snap
	return true
}

// keepRunning ends the loop after idleStop with no caller. The stale snapshot
// goes with it, so the next caller waits for a fresh one.
func (s *Sampler) keepRunning(stop <-chan struct{}) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-stop:
		return false
	default:
	}
	if time.Since(s.lastCall) <= s.idleStop {
		return true
	}
	s.running = false
	s.snap = nil
	return false
}

// refreshDisks reads filesystems on a goroutine of its own, at most one at a
// time, because statfs on a dead mount can block for as long as it likes.
func (s *Sampler) refreshDisks() {
	s.mu.Lock()
	if s.disksBusy || time.Since(s.disksAt) < diskEvery {
		s.mu.Unlock()
		return
	}
	s.disksBusy = true
	s.mu.Unlock()
	go func() {
		disks := readDisks(s.proc)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.disks, s.disksBusy, s.disksAt = disks, false, time.Now()
	}()
}

func sleep(stop <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-stop:
		return false
	case <-t.C:
		return true
	}
}
