package images

import (
	"context"
	"log"
	"time"
)

// maintainEvery is how often the maintainer checks whether a refresh is due.
const maintainEvery = time.Hour

// Maintainer keeps the scanner databases fresh and re-scans the approved
// digests' SBOMs whenever the Trivy database changed: after its own refresh,
// or after the laptop pushed one.
type Maintainer struct {
	Vetter    *Vetter
	Refresher Refresher
	// Image builds the scanner image the refresh job runs in.
	Image *ScannerImage
	// Every is harness.images.db_refresh (24h floor).
	Every time.Duration

	rescanned time.Time
}

// Run checks once a minute after start, then every hour, until ctx ends.
func (m *Maintainer) Run(ctx context.Context) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		m.Step(ctx)
		timer.Reset(maintainEvery)
	}
}

// Step refreshes the databases when they are due and re-scans when the
// Trivy database is newer than the last re-scan.
func (m *Maintainer) Step(ctx context.Context) {
	meta, err := ReadDBMeta(m.Refresher.DBDir)
	if err != nil {
		log.Printf("harness images: scanner databases: %v", err)
	}
	before := meta[DBTrivy].UpdatedAt
	if m.rescanned.IsZero() {
		// After a start, only a database newer than the one found counts.
		m.rescanned = before
	}
	if meta.RefreshDue(m.Refresher.now(), m.Every) {
		m.refresh(ctx)
	}
	if meta, err = ReadDBMeta(m.Refresher.DBDir); err != nil {
		return
	}
	if at := meta[DBTrivy].UpdatedAt; at.After(m.rescanned) {
		m.rescanned = at
		m.Vetter.Rescan(ctx)
	}
}

func (m *Maintainer) refresh(ctx context.Context) {
	r := m.Refresher
	if m.Image != nil {
		id, err := m.Image.Ensure(ctx)
		if err != nil {
			log.Printf("harness images: scanner image: %v", err)
			return
		}
		r.Image = id
	}
	if err := r.Refresh(ctx); err != nil {
		log.Printf("harness images: database refresh: %v", err)
	}
}
