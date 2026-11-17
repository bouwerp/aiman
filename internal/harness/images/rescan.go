package images

import (
	"context"
	"log"
	"os"
	"slices"
)

// Rescan re-scans the stored SBOM of every approved digest with the current
// databases. A new critical CVE (any CVE at cve_block high), or a new MAL- id,
// marks the digest stale and tells the notifier; new high CVEs only update
// the record. Running containers are left alone and no image is deleted.
func (v *Vetter) Rescan(ctx context.Context) {
	recs, err := v.o.Store.List()
	if err != nil {
		log.Printf("harness images: rescan: %v", err)
		return
	}
	for _, rec := range recs {
		if rec.State != StateApproved || rec.Scan.SBOM == "" {
			continue
		}
		if _, err := os.Stat(rec.Scan.SBOM); err != nil {
			continue
		}
		res, err := v.o.Scanner.Scan(ctx, ScanRequest{Digest: rec.Digest, Input: rec.Scan.SBOM, Stage: ScanStageSBOM,
			Tools: []string{ToolTrivy, ToolOSV}, MaxBytes: 64 << 20, Timeout: v.o.ScanTimeout})
		if err != nil {
			log.Printf("harness images: rescan %s: %v", ShortDigest(rec.Digest), err)
			continue
		}
		v.applyRescan(rec, res)
	}
}

func (v *Vetter) applyRescan(rec Record, res StageResult) {
	newCrit := NewIDs(rec.Scan.Critical, res.Critical)
	if v.o.CVEBlock == BlockHigh {
		newCrit = append(newCrit, NewIDs(append(rec.Scan.High, rec.Scan.Critical...), res.High)...)
	}
	newMal := NewIDs(rec.Scan.MalIDs, res.MalIDs)
	newIDs := slices.Concat(newCrit, newMal)
	saved, err := v.o.Store.Update(rec.Digest, func(r *Record, _ bool) error {
		if r.State != StateApproved {
			return errSkip
		}
		r.RescannedAt = v.o.Now()
		r.Scan.TrivyDB = res.TrivyDB
		r.Scan.High = res.High
		r.Scan.Critical = res.Critical
		r.Scan.MalIDs = res.MalIDs
		if len(newIDs) > 0 {
			r.State = StateStale
			r.Reasons = []string{"re-scan found " + firstIDs(newIDs, 20)}
		}
		return nil
	})
	if err != nil {
		log.Printf("harness images: rescan %s: %v", ShortDigest(rec.Digest), err)
		return
	}
	if saved.State == StateStale && len(newIDs) > 0 {
		v.o.Notify.Stale(saved, newIDs)
	}
}
