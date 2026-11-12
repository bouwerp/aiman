package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

// dumpStackCap bounds the goroutine stack section of a dump; a cut dump ends
// with a line saying it was truncated.
var dumpStackCap = 64 << 20

// dumpStack is runtime.Stack; tests swap it.
var dumpStack = runtime.Stack

// dumpStackStart is the first stack buffer size; it doubles up to the cap.
const dumpStackStart = 1 << 20

// ErrDumpInProgress is WriteDump's answer while another dump is running.
var ErrDumpInProgress = errors.New("dump in progress")

const (
	// autoDumpAfter is how long in flight must sit at the cap before serve
	// dumps on its own.
	autoDumpAfter = 30 * time.Second
	// autoDumpEvery spaces automatic dumps.
	autoDumpEvery = 10 * time.Minute
	// autoDumpCheck is how often the automatic dump reads in flight.
	autoDumpCheck = 2 * time.Second
)

// dumping guards WriteDump across every Server in the process: the stacks are
// the process's, and one dump at a time is the point.
var dumping atomic.Bool

// WriteDump writes <dir>/serve-dump-<unix>.txt (0600): RPCStats as JSON, the
// load average, the in-flight calls, then every goroutine's stack up to
// dumpStackCap. A call while another dump runs returns ErrDumpInProgress and
// writes nothing.
func (s *Server) WriteDump(dir string) (string, error) {
	if !dumping.CompareAndSwap(false, true) {
		return "", ErrDumpInProgress
	}
	defer dumping.Store(false)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	stats, err := json.MarshalIndent(s.RPCStats(), "", "  ")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "aiman serve dump %s pid %d\n\nrpc stats:\n%s\n\n", time.Now().UTC().Format(time.RFC3339), os.Getpid(), stats)
	if load, ok := readLoadAvg(); ok {
		fmt.Fprintf(&b, "load average: %.2f\n\n", load)
	} else {
		b.WriteString("load average: unavailable\n\n")
	}
	calls := s.InflightCalls()
	fmt.Fprintf(&b, "in-flight calls: %d\n", len(calls))
	for _, c := range calls {
		fmt.Fprintf(&b, "  %s\t%s\t%s\n", c.Method, c.Caller, c.Age.Round(time.Millisecond))
	}
	b.WriteString("\ngoroutines:\n")
	stack, cut := captureStacks()

	return writeDumpFile(dir, time.Now().Unix(), []byte(b.String()), stack, cut)
}

// writeDumpFile writes the dump to serve-dump-<unix>.txt.tmp, syncs it and
// renames it into place, so a reader never sees a half-written dump.
func writeDumpFile(dir string, unix int64, head, stack []byte, cut bool) (string, error) {
	f, path, err := createDumpFile(dir, unix)
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	_, err = f.Write(head)
	if err == nil {
		_, err = f.Write(stack)
	}
	if err == nil && cut {
		_, err = fmt.Fprintf(f, "\n... goroutine stacks truncated at %d bytes\n", len(stack))
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return path, nil
}

// captureStacks is every goroutine's stack, growing the buffer up to
// dumpStackCap; cut reports a stack that did not fit.
func captureStacks() ([]byte, bool) {
	size := min(dumpStackStart, dumpStackCap)
	for {
		buf := make([]byte, size)
		n := dumpStack(buf, true)
		if n < len(buf) {
			return buf[:n], false
		}
		if size >= dumpStackCap {
			return buf[:n], true
		}
		size = min(size*2, dumpStackCap)
	}
}

// createDumpFile opens serve-dump-<unix>.txt.tmp (0600, O_EXCL) and returns
// it with the final name, moving to the next second's name when a dump of
// that second already exists.
func createDumpFile(dir string, unix int64) (*os.File, string, error) {
	for i := int64(0); ; i++ {
		path := filepath.Join(dir, fmt.Sprintf("serve-dump-%d.txt", unix+i))
		_, statErr := os.Lstat(path)
		if statErr == nil && i < 60 {
			continue
		}
		f, err := os.OpenFile(path+".tmp", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return f, path, nil
		}
		if !errors.Is(err, fs.ErrExist) || i >= 60 {
			return nil, "", err
		}
	}
}

// autoDump decides when in flight has sat at the cap long enough to dump.
type autoDump struct {
	capSince time.Time // when the current run at the cap began; zero below it
	lastDump time.Time
}

// due is true once in flight has been at the cap for autoDumpAfter, then not
// again for autoDumpEvery. A reading below the cap restarts the clock.
func (d *autoDump) due(now time.Time, atCap bool) bool {
	if !atCap {
		d.capSince = time.Time{}
		return false
	}
	if d.capSince.IsZero() {
		d.capSince = now
	}
	if now.Sub(d.capSince) < autoDumpAfter {
		return false
	}
	if !d.lastDump.IsZero() && now.Sub(d.lastDump) < autoDumpEvery {
		return false
	}
	d.lastDump = now
	return true
}

// HandleDumpSignals writes a dump into dir on every SIGUSR1 until ctx ends,
// and on its own when in flight sits at the cap (autoDump). A signal that
// arrives during a dump is dropped; one that lands during an auto dump makes
// aiman host dump report the auto dump's file.
func (s *Server) HandleDumpSignals(ctx context.Context, dir string) {
	sig := make(chan os.Signal, 1)
	if len(dumpSignals) > 0 {
		// Notify with no signals would relay every signal, so a platform
		// without SIGUSR1 (Windows) gets the auto dump only.
		signal.Notify(sig, dumpSignals...)
		defer signal.Stop(sig)
	}
	go s.runAutoDump(ctx, dir)
	for {
		select {
		case <-ctx.Done():
			return
		case <-sig:
			s.logDump("SIGUSR1", dir)
			select {
			case <-sig:
			default:
			}
		}
	}
}

// runAutoDump checks in flight against the cap every autoDumpCheck.
func (s *Server) runAutoDump(ctx context.Context, dir string) {
	t := time.NewTicker(autoDumpCheck)
	defer t.Stop()
	var d autoDump
	a := s.admission()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if d.due(now, a.atCap()) {
				s.logDump("in flight at the cap", dir)
			}
		}
	}
}

func (s *Server) logDump(why, dir string) {
	path, err := s.WriteDump(dir)
	switch {
	case errors.Is(err, ErrDumpInProgress):
	case err != nil:
		log.Printf("serve dump (%s): %v", why, err)
	default:
		log.Printf("serve dump (%s): %s", why, path)
	}
}
