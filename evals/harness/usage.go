package harness

import (
	"context"
	"fmt"
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
	h.usage = usage.NewTracker(h.Repo, h.Repo, nil)
	h.usage.SetContextWindow(window)
	h.usage.SetBudgetHandler(h.srv.PublishBudget)
	_, err := h.usage.ScanOnce(context.Background())
	return err
}

// ScanUsage runs one more tracker pass after the transcript changes.
func (h *Harness) ScanUsage() error {
	if h.usage == nil {
		return fmt.Errorf("usage is not enabled")
	}
	_, err := h.usage.ScanOnce(context.Background())
	return err
}
