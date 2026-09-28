package ptyruntime

import (
	"bytes"
	"strings"
	"sync"
	"time"

	"github.com/hinshun/vt10x"

	"github.com/bouwerp/aiman/internal/ptyhold"
)

// screenIdleTTL is how long a session's emulator is kept after its last
// capture. The dashboard previews one session at a time but classification asks
// about every live one, so this bounds how many emulators are held at once
// without throwing away the state of a session being actively watched.
const screenIdleTTL = 10 * time.Minute

// scrollbackLines bounds the journaled history a preview or read can scroll
// back through, the way tmux's history-limit bounds `capture-pane -S -`.
const scrollbackLines = 2000

// screen is a session's terminal emulator, plus how much of the session's
// output has been fed into it.
//
// Rendering a screen used to mean replaying the entire spool through a fresh
// emulator, which is linear in the session's whole lifetime: ~1.3 seconds for a
// 6 MB spool on a real remote, against ~10 ms for the tmux equivalent, and the
// dashboard asks twice a second. Keeping the emulator and feeding it only the
// bytes that arrived since last time makes a capture cost the same as the output
// since the previous one — a few kilobytes.
type screen struct {
	mu       sync.Mutex
	term     vt10x.Terminal
	cols     int
	rows     int
	consumed int64 // length of the spool stream already applied
	lastUsed time.Time
	// hist journals the lines that scrolled off the grid, oldest first.
	// vt10x keeps no scrollback itself, and replaying the spool through a
	// taller emulator is not equivalent: cursor-addressing agents lay out
	// against the session's real size, so a wrong-height replay smears
	// repaints down the grid instead of converging on what was shown. The
	// journal records each exited line as it leaves the native-size grid,
	// which is exactly what a preview scrolls back through.
	hist []string
}

// capture brings the emulator up to date and renders it.
func (s *screen) capture(root, id string, cols, rows int) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	// A resize reflows everything, so the screen is rebuilt rather than
	// reflowed: vt10x does not reflow existing content the way the agent's own
	// repaint will, and a stale-width screen is worse than a slow one.
	// A rebuild also skips journaling: the replayed bytes re-establish the
	// same picture rather than producing new output, so diffing them could
	// mistake the restored picture for scrolled lines.
	rebuilt := false
	if s.term == nil || s.cols != cols || s.rows != rows {
		s.term = newTerminal(cols, rows)
		s.cols, s.rows = cols, rows
		s.consumed = 0
		rebuilt = true
	}

	data, total := ptyhold.ReadSpoolFrom(root, id, s.consumed)
	if total < s.consumed {
		// The spool rotated, so the offset no longer means anything: ReadSpoolFrom
		// has handed back the whole retained stream, and it has to go through a
		// fresh emulator or it would be applied on top of a screen that already
		// contains some of it.
		s.term = newTerminal(cols, rows)
		s.cols, s.rows = cols, rows
		rebuilt = true
	}
	if len(data) > 0 {
		if rebuilt {
			_, _ = s.term.Write(data)
		} else {
			s.feedJournaled(data, cols, rows)
		}
	}
	s.consumed = total
	s.lastUsed = time.Now()

	return renderTerminal(s.term, cols, rows)
}

// captureScrollback brings the emulator up to date like capture, then returns
// the journaled history followed by the live screen.
func (s *screen) captureScrollback(root, id string, cols, rows int) string {
	text := s.capture(root, id, cols, rows)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.hist) == 0 {
		return text
	}
	rowsOut := make([]string, 0, len(s.hist)+rows)
	rowsOut = append(rowsOut, s.hist...)
	if text != "" {
		rowsOut = append(rowsOut, strings.Split(text, "\n")...)
	}
	return strings.Join(rowsOut, "\n")
}

// journalPieceBytes caps one observed feed piece. A scroll followed by a
// repaint in the same unobserved window is invisible to the before/after
// diff, and streaming agents repaint constantly — so the feed is split at
// line ends and each piece diffed, keeping scrolls and their repaints in
// different windows. The byte cap covers repaint frames with no newline: a
// piece never holds more than a few wrapped rows, so the scrolls inside one
// piece stay observable. Parser state persists across Writes, so splitting
// mid-sequence is safe; the returned count resubmits a rune split across
// pieces.
const journalPieceBytes = 256

// feedJournaled feeds new spool bytes piece by piece, journaling the lines
// each piece scrolls off the grid. Write takes the emulator's own lock and
// screenRows takes it too, so the two must not be nested.
func (s *screen) feedJournaled(data []byte, cols, rows int) {
	before := screenRows(s.term, cols, rows)
	for len(data) > 0 {
		n := pieceLength(data)
		written, _ := s.term.Write(data[:n])
		if written <= 0 {
			written = 1 // invalid byte: drop it rather than spin
		}
		if written > n {
			written = n
		}
		data = data[written:]
		after := screenRows(s.term, cols, rows)
		s.journalScroll(before, after)
		before = after
	}
}

// pieceLength ends a feed piece after the first newline or at the byte cap,
// whichever comes first.
func pieceLength(data []byte) int {
	n := len(data)
	if n > journalPieceBytes {
		n = journalPieceBytes
	}
	if i := bytes.IndexByte(data[:n], '\n'); i >= 0 {
		return i + 1
	}
	return n
}

// journalScroll records the lines that left the grid between two renders of
// the same emulator. An upward shift of k rows inside some interval means the
// k lines at that interval's top scrolled off: they are no longer on the
// grid, so they move to the journal. The interval is found, not assumed:
// rows outside an agent's scroll region never move, which pins the interval
// to the region without knowing its bounds. Anything else — in-place
// repaints, clears, downward scrolls, alt-screen switches — matches no shift
// and journals nothing, which is correct: that content is either still
// visible or was never scrolled.
func (s *screen) journalScroll(before, after []string) {
	n := len(after)
	if len(before) != n || n == 0 {
		return
	}
	// Work toward after, adopting each journaled interval, so two regions
	// scrolling in one segment are both found.
	work := append([]string(nil), before...)
	for {
		a, b, k, ok := findScroll(work, after)
		if !ok {
			return
		}
		s.journalLines(work[a : a+k])
		copy(work[a:b], after[a:b])
	}
}

// findScroll locates one upward shift of k rows inside [a, b]: the block
// after[a:b-k] still matches before[a+k:b], while the rows outside the
// interval are unchanged. The compared block must be non-empty — an empty
// comparison matches vacuously and would journal static rows every frame.
// Smaller shifts win: under periodic content a larger k can also match, but
// journaling it would duplicate lines that are still on the grid.
func findScroll(before, after []string) (a, b, k int, ok bool) {
	n := len(after)
	first, last := -1, -1
	for i := 0; i < n; i++ {
		if before[i] != after[i] {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return 0, 0, 0, false
	}
	for k = 1; k < n; k++ {
		for a = 0; a <= first; a++ {
			for b = n; b > last; b-- {
				if b-k <= a {
					continue
				}
				if rowsEqual(after[a:b-k], before[a+k:b]) {
					return a, b, k, true
				}
			}
		}
	}
	return 0, 0, 0, false
}

// journalLines appends exited lines to the journal, dropping the oldest past
// the cap. An all-blank exit journals nothing: padding scrolling off a
// half-empty screen is not transcript.
func (s *screen) journalLines(lines []string) {
	blank := true
	for _, l := range lines {
		if l != "" {
			blank = false
			break
		}
	}
	if blank {
		return
	}
	s.hist = append(s.hist, lines...)
	if len(s.hist) > scrollbackLines {
		s.hist = append([]string(nil), s.hist[len(s.hist)-scrollbackLines:]...)
	}
}

// rowsEqual reports whether two row runs are identical.
func rowsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// screenFor returns the session's emulator, creating one if needed, and drops
// any that have gone idle.
func (m *Manager) screenFor(id string) *screen {
	m.screenMu.Lock()
	defer m.screenMu.Unlock()
	if m.screens == nil {
		m.screens = map[string]*screen{}
	}
	m.reapIdleScreensLocked()
	s, ok := m.screens[id]
	if !ok {
		s = &screen{lastUsed: time.Now()}
		m.screens[id] = s
	}
	return s
}

// reapIdleScreensLocked drops emulators for sessions nobody is looking at.
// Callers hold screenMu.
func (m *Manager) reapIdleScreensLocked() {
	cutoff := time.Now().Add(-screenIdleTTL)
	for id, s := range m.screens {
		// A screen mid-capture is in use by definition; skip rather than block.
		if !s.mu.TryLock() {
			continue
		}
		idle := !s.lastUsed.IsZero() && s.lastUsed.Before(cutoff)
		s.mu.Unlock()
		if idle {
			delete(m.screens, id)
		}
	}
}

// dropScreen forgets a session's emulator. Called when a session goes away, so
// a later session reusing the id cannot inherit its screen.
func (m *Manager) dropScreen(id string) {
	m.screenMu.Lock()
	defer m.screenMu.Unlock()
	delete(m.screens, id)
}
