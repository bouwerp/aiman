package harness

import (
	"context"
	"time"

	"github.com/bouwerp/aiman/internal/usage"
)

// EnableUsage attaches a real usage reporter to the harness server and scans
// the session transcripts once. window is the explicit context window; zero
// falls back to the confirmed table or the 200k default.
func (h *Harness) EnableUsage(window int) error {
	h.srv.EnableUsage(context.Background(), nil, usage.Reporter{
		Store:  h.Repo,
		Prices: usage.DefaultPrices(),
		Now:    time.Now,
	})
	tracker := usage.NewTracker(h.Repo, h.Repo, nil)
	tracker.SetContextWindow(window)
	_, err := tracker.ScanOnce(context.Background())
	return err
}
