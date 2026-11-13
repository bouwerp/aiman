package selfupdate

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const (
	// watchEvery paces the watcher that settles an install failing before
	// serve restarts.
	watchEvery = 2 * time.Second
	// finishEvery paces Finish while it waits for the Result.
	finishEvery = 500 * time.Millisecond
)

// Finish settles a started attempt, and a started operation, from their
// Result files. It waits until both settle, ResultWait after each start
// passes, or ctx ends. serve runs it once at start.
func (m *Manager) Finish(ctx context.Context) {
	for {
		if !m.settleOnce() {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(finishEvery):
		}
	}
}

// watch settles the attempt this process just started, so an install that
// fails before serve restarts is recorded by the serve still running.
func (m *Manager) watch() {
	deadline := time.Now().Add(ResultWait + watchEvery)
	t := time.NewTicker(watchEvery)
	defer t.Stop()
	for range t.C {
		if !m.settleOnce() || time.Now().After(deadline) {
			return
		}
	}
}

// settleOnce settles under the operation lock and reports whether a started
// attempt or operation younger than ResultWait is still waiting.
func (m *Manager) settleOnce() bool {
	m.opMu.Lock()
	s := m.load()
	settled := m.settleLocked(&s)
	m.settleOpLocked(&s)
	waiting := false
	for _, a := range []*Attempt{s.Attempt, s.Operation} {
		if started(a) && m.now().Sub(a.At) < ResultWait {
			waiting = true
		}
	}
	m.opMu.Unlock()
	var ev events
	ev.add(settled)
	m.fire(ev)
	return waiting
}

// settleLocked decides a started attempt from the Result file and saves it.
// It returns the settled attempt for OnApplied, nil when there is nothing to
// settle yet. A Result for another tag, older than the attempt or unparsable
// reads as not yet.
func (m *Manager) settleLocked(s *State) *Attempt {
	a := s.Attempt
	if a == nil || a.Status != AttemptStarted {
		return nil
	}
	res, ok, err := ReadResult(m.ResultPath)
	if err != nil {
		m.logf("host-update: read %s: %v", m.ResultPath, err)
	}
	// The wrapper's time has whole seconds.
	valid := ok && res.Tag == a.To && !res.At.Before(a.At.Truncate(time.Second))
	waited := m.now().Sub(a.At) >= ResultWait
	atTarget := m.Version == a.To
	switch {
	case valid && res.Exit == 0 && atTarget:
		a.Status, a.Exit, a.Error = AttemptOK, 0, ""
	case valid && res.Exit != 0 && a.IsGateway():
		a.Status, a.Exit = AttemptFailed, res.Exit
		a.Error = withLogLine(fmt.Sprintf("gateway install failed (exit %d)", res.Exit), m.LogPath)
	case valid && res.Exit != 0 && atTarget:
		a.Status, a.Exit = AttemptFailed, res.Exit
		a.Error = withLogLine(fmt.Sprintf("gateway lockstep failed (exit %d)", res.Exit), m.LogPath)
	case valid && res.Exit != 0:
		a.Status, a.Exit = AttemptFailed, res.Exit
		a.Error = withLogLine(fmt.Sprintf("install failed (exit %d)", res.Exit), m.LogPath)
	case valid:
		if !waited {
			return nil
		}
		a.Status, a.Error = AttemptFailed, fmt.Sprintf("restart did not happen (running %s)", m.Version)
	default:
		if !waited || Alive(a.PID) {
			return nil
		}
		a.Status, a.Error = AttemptFailed, fmt.Sprintf("no result (running %s)", m.Version)
	}
	m.save(*s)
	if a.Status == AttemptOK {
		m.logf("host-update: updated %s -> %s", a.From, a.To)
	} else {
		m.logf("host-update: %s -> %s failed: %s", a.From, a.To, a.Error)
	}
	out := *a
	return &out
}

// withLogLine appends the log's last non-empty line to msg.
func withLogLine(msg, logPath string) string {
	if line := lastLine(logPath); line != "" {
		return msg + ": " + line
	}
	return msg
}

// lastLine returns the last non-empty line of the file's final 4 KiB, cut to
// 200 bytes.
func lastLine(path string) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path) //nolint:gosec // G304: the caller owns the log path
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	const tail = 4096
	if info, err := f.Stat(); err == nil && info.Size() > tail {
		_, _ = f.Seek(info.Size()-tail, io.SeekStart)
	}
	b, err := io.ReadAll(io.LimitReader(f, tail))
	if err != nil {
		return ""
	}
	lines := bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(string(lines[i])); line != "" {
			if len(line) > 200 {
				line = line[:200]
			}
			return line
		}
	}
	return ""
}
