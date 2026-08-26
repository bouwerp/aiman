package ptyruntime

import (
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
}

// capture brings the emulator up to date and renders it.
func (s *screen) capture(root, id string, cols, rows int) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	// A resize reflows everything, so the screen is rebuilt rather than
	// reflowed: vt10x does not reflow existing content the way the agent's own
	// repaint will, and a stale-width screen is worse than a slow one.
	if s.term == nil || s.cols != cols || s.rows != rows {
		s.term = newTerminal(cols, rows)
		s.cols, s.rows = cols, rows
		s.consumed = 0
	}

	data, total := ptyhold.ReadSpoolFrom(root, id, s.consumed)
	if total < s.consumed {
		// The spool rotated, so the offset no longer means anything: ReadSpoolFrom
		// has handed back the whole retained stream, and it has to go through a
		// fresh emulator or it would be applied on top of a screen that already
		// contains some of it.
		s.term = newTerminal(cols, rows)
		s.cols, s.rows = cols, rows
	}
	if len(data) > 0 {
		// Write takes the emulator's own lock; renderTerminal takes it too, so
		// they must not be nested.
		_, _ = s.term.Write(data)
	}
	s.consumed = total
	s.lastUsed = time.Now()

	return renderTerminal(s.term, cols, rows)
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
