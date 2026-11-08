package selfupdate

import (
	"context"
	"time"
)

// Run polls FirstProbeDelay after it starts, then every CheckEvery. While the
// gate defers it looks again every DeferRetry, without asking GitHub, for up
// to CheckEvery; then the next poll takes over. Mode off and a dev build
// return at once.
func (m *Manager) Run(ctx context.Context) {
	if m.Policy.mode() == ModeOff || m.dev() {
		return
	}
	every, retry := m.Policy.checkEvery(), orDefault(m.DeferRetry, DefaultDeferRetry)
	wait := orDefault(m.FirstProbeDelay, DefaultFirstProbeDelay)
	var deferredFor time.Duration
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		st := m.poll(ctx, deferredFor == 0)
		wait = every
		if st.Status == StatusDeferred && deferredFor < every {
			wait = retry
			deferredFor += retry
		} else {
			deferredFor = 0
		}
	}
}

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}
