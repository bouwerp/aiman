package limits

import "time"

// Manager caches readings and runs due sources. Stub until P1.
type Manager struct{}

// NewManager returns a Manager over sources.
func NewManager(_ []Source, _ func() time.Time) *Manager { return &Manager{} }

// Snapshot returns the cached readings at once and starts whatever is due.
// force skips the age check and joins a run already in flight.
func (m *Manager) Snapshot(_ bool) Snapshot { return Snapshot{} }
