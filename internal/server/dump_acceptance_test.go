package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/ptyruntime"
)

// Serve load guard P5 (docs/specs/serve-load-guard.md, Behaviour 6): a dump of
// what serve is doing lands in a file the journal cannot drop.
//
// Contract pinned here beyond the design's Contracts block:
//   - func (s *Server) WriteDump(dir string) (string, error) writes
//     <dir>/serve-dump-<unix>.txt (mode 0600): RPCStats as JSON, the load
//     average, the in-flight calls (method, caller, age), then every
//     goroutine's stack. One dump at a time: a second call while one runs
//     returns ErrDumpInProgress and writes nothing.
//   - var dumpStackCap = 64 << 20 bounds the stack section; a truncated dump
//     ends with a line containing "truncated". var dumpStack is the
//     runtime.Stack seam tests swap.
//   - func (s *Server) InflightCalls() []InflightCall lists admitted calls
//     still running, oldest first; InflightCall{Method, Caller string; Age time.Duration}.
//   - type autoDump with (d *autoDump) due(now time.Time, atCap bool) bool:
//     true once inflight has sat at the cap for autoDumpAfter (30s), then not
//     again for autoDumpEvery (10m); a reading below the cap resets the clock.
//   - func (s *Server) HandleDumpSignals(ctx context.Context, dir string)
//     writes a dump on SIGUSR1 until ctx ends; a signal during a dump is ignored.

var dumpName = regexp.MustCompile(`^serve-dump-\d+\.txt$`)

func TestWriteDumpFileAndContents(t *testing.T) {
	srv, _, _ := ptyServer(t)
	dir := t.TempDir()
	path, err := srv.WriteDump(dir)
	if err != nil {
		t.Fatalf("WriteDump: %v", err)
	}
	if filepath.Dir(path) != dir || !dumpName.MatchString(filepath.Base(path)) {
		t.Fatalf("path = %q, want <dir>/serve-dump-<unix>.txt", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{`"window_seconds"`, "load average", "in-flight", "goroutine ", "TestWriteDumpFileAndContents"} {
		if !strings.Contains(body, want) {
			t.Errorf("dump lacks %q", want)
		}
	}
	if strings.Contains(strings.ToLower(body), "truncated") {
		t.Errorf("a small dump claims truncation")
	}
}

func TestWriteDumpOneAtATime(t *testing.T) {
	old := dumpStack
	dumpStack = func(buf []byte, all bool) int {
		time.Sleep(300 * time.Millisecond)
		return runtime.Stack(buf, all)
	}
	defer func() { dumpStack = old }()
	srv, _, _ := ptyServer(t)
	dir := t.TempDir()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = srv.WriteDump(dir)
		}(i)
	}
	wg.Wait()
	busy := 0
	for _, err := range errs {
		switch {
		case err == nil:
		case errors.Is(err, ErrDumpInProgress):
			busy++
		default:
			t.Fatalf("WriteDump: %v", err)
		}
	}
	if busy != 1 {
		t.Fatalf("%d of 2 concurrent dumps refused, want exactly 1", busy)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("%d files written, want 1", len(entries))
	}
}

func TestWriteDumpTruncatesAtTheCap(t *testing.T) {
	if dumpStackCap != 64<<20 {
		t.Fatalf("dumpStackCap = %d, want 64 MiB", dumpStackCap)
	}
	oldCap, oldStack := dumpStackCap, dumpStack
	dumpStackCap = 4096
	dumpStack = func(buf []byte, _ bool) int {
		for i := range buf {
			buf[i] = 'x'
		}
		return len(buf)
	}
	defer func() { dumpStackCap, dumpStack = oldCap, oldStack }()
	srv, _, _ := ptyServer(t)
	path, err := srv.WriteDump(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if last := lines[len(lines)-1]; !strings.Contains(strings.ToLower(last), "truncated") {
		t.Fatalf("last line = %q, want it to say the stack was truncated", last)
	}
}

func TestWriteDumpListsInflightCalls(t *testing.T) {
	srv, pty, _ := ptyServer(t, sweepRow("busy-1"))
	pty.Add(ptyruntime.SessionInfo{ID: "busy-1", Name: "busy-1", Dir: "/w/busy-1", Status: ptyruntime.StatusRunning})
	pty.SetCaptureDelay(2 * time.Second)
	done := make(chan Response, 1)
	go func() {
		done <- admCall(context.Background(), t, srv, "", 4242, "pty.capture", map[string]any{"id": "busy-1", "lines": 10})
	}()
	waitFor(t, "pty.capture in flight", func() bool {
		for _, c := range srv.InflightCalls() {
			if c.Method == "pty.capture" {
				return true
			}
		}
		return false
	})
	calls := srv.InflightCalls()
	if calls[0].Age < 0 || calls[0].Caller == "" {
		t.Errorf("InflightCalls()[0] = %+v, want a non-negative age and a caller", calls[0])
	}
	path, err := srv.WriteDump(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "pty.capture") {
		t.Errorf("dump does not list the in-flight pty.capture")
	}
	<-done
	if n := len(srv.InflightCalls()); n != 0 {
		t.Errorf("InflightCalls() = %d after the call returned, want 0", n)
	}
}

func TestAutoDumpDecision(t *testing.T) {
	if autoDumpAfter != 30*time.Second || autoDumpEvery != 10*time.Minute {
		t.Fatalf("autoDumpAfter/autoDumpEvery = %v/%v, want 30s/10m", autoDumpAfter, autoDumpEvery)
	}
	t0 := time.Date(2026, 11, 9, 17, 0, 0, 0, time.UTC)
	var d autoDump
	steps := []struct {
		at    time.Duration
		atCap bool
		want  bool
	}{
		{0, true, false},
		{29 * time.Second, true, false},
		{30 * time.Second, true, true},
		{31 * time.Second, true, false},
		{5 * time.Minute, true, false},
		{10*time.Minute + 30*time.Second, true, true},
		{10*time.Minute + 31*time.Second, true, false},
	}
	for _, s := range steps {
		if got := d.due(t0.Add(s.at), s.atCap); got != s.want {
			t.Errorf("due(+%v, atCap=%v) = %v, want %v", s.at, s.atCap, got, s.want)
		}
	}
	var r autoDump
	r.due(t0, true)
	r.due(t0.Add(20*time.Second), false) // dropped below the cap: the clock resets
	if r.due(t0.Add(40*time.Second), true) {
		t.Error("due 20s after the cap was regained, want the 30s to start over")
	}
	if !r.due(t0.Add(70*time.Second), true) {
		t.Error("not due 30s after the cap was regained")
	}
}

func TestSIGUSR1WritesADump(t *testing.T) {
	srv, _, _ := ptyServer(t)
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.HandleDumpSignals(ctx, dir)
	time.Sleep(100 * time.Millisecond)
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		entries, _ := os.ReadDir(dir)
		if len(entries) == 1 && dumpName.MatchString(entries[0].Name()) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no dump file in %s after SIGUSR1: %v", dir, entries)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
