package images

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/harness"
)

// ApproveTimeout bounds one approval: fetch again when needed, verify, load.
const ApproveTimeout = 10 * time.Minute

// ApproveRequest approves one digest.
type ApproveRequest struct {
	Digest string
	// By is session, operator, config or operator_run.
	By string
	// Approver names who approved: a session name, or "operator".
	Approver string
	Quote    string
	// AcceptRisk approves a blocked, failed or stale digest; only the
	// operator sends it, with a Reason.
	AcceptRisk bool
	Reason     string
}

// ErrNotPending is an approval of a digest that is not pending_approval,
// without accept-risk.
var ErrNotPending = errors.New("image is not pending approval")

func (v *Vetter) digestLock(d string) *sync.Mutex {
	m, _ := v.digests.LoadOrStore(d, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// Approve approves a digest: pending_approval, or with AcceptRisk a blocked,
// failed or stale one. It loads the image from the quarantine (fetching the
// same digest again when the quarantine is gone), checks the loaded image ID
// against the scanned digest, records the approval, deletes the quarantine,
// and tells the notifier, which closes every gate for the digest.
func (v *Vetter) Approve(ctx context.Context, req ApproveRequest) (Record, error) {
	rec, err := v.o.Store.Get(req.Digest)
	if err != nil {
		return Record{}, err
	}
	switch {
	case rec.State == StateApproved:
		return rec, nil
	case rec.State == StatePendingApproval && rec.Stage != StageLoad:
	case req.AcceptRisk && (rec.State == StateBlocked || rec.State == StateFailed || rec.State == StateStale):
	default:
		return rec, fmt.Errorf("%w: %s is %s", ErrNotPending, ShortDigest(rec.Digest), rec.State)
	}
	return v.approve(ctx, rec, req)
}

// goApprove approves in the background (the operator's own run of a clean
// digest).
func (v *Vetter) goApprove(req ApproveRequest) {
	v.wg.Add(1)
	go func() {
		defer v.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), ApproveTimeout)
		defer cancel()
		if _, err := v.Approve(ctx, req); err != nil && !errors.Is(err, ErrNotPending) {
			log.Printf("harness images: approve %s: %v", ShortDigest(req.Digest), err)
		}
	}()
}

func (v *Vetter) approve(ctx context.Context, rec Record, req ApproveRequest) (Record, error) {
	lock := v.digestLock(rec.Digest)
	lock.Lock()
	defer lock.Unlock()
	if cur, err := v.o.Store.Get(rec.Digest); err == nil && cur.State == StateApproved {
		return cur, nil
	}
	if _, err := v.o.Store.Update(rec.Digest, func(r *Record, _ bool) error {
		r.Stage = StageLoad
		return nil
	}); err != nil {
		return rec, err
	}
	if rec.State != StateStale || rec.ImageID == "" {
		res, err := v.o.Registry.Resolve(ctx, rec.Repo+"@"+rec.Digest, v.o.Platform)
		if err == nil {
			err = v.loadVerified(ctx, &rec, res)
		}
		if err != nil {
			v.fail(rec.Digest, "load: "+err.Error())
			return rec, err
		}
	}
	highs := append([]string(nil), rec.Scan.High...)
	saved, err := v.o.Store.Update(rec.Digest, func(r *Record, _ bool) error {
		r.State, r.Stage, r.At = StateApproved, "", v.o.Now()
		r.ImageID, r.LocalName = rec.ImageID, rec.LocalName
		r.ApprovedBy, r.Approver, r.Quote = req.By, req.Approver, strings.TrimSpace(req.Quote)
		r.AcceptedHighs, r.AcceptRisk, r.Reason = highs, req.AcceptRisk, strings.TrimSpace(req.Reason)
		return nil
	})
	if err != nil {
		return rec, err
	}
	_ = os.RemoveAll(v.QuarantineDir(rec.Digest))
	v.o.Notify.Approved(saved)
	return saved, nil
}

// loadVerified loads the quarantined layout into the engine, fetching the
// digest into the quarantine first when it is not there, and checks that
// the engine's image ID names the scanned bytes. A mismatch removes the
// loaded image.
func (v *Vetter) loadVerified(ctx context.Context, rec *Record, res Resolved) error {
	q := v.QuarantineDir(rec.Digest)
	if _, err := Verify(q, rec.Digest); err != nil {
		if res.Digest != rec.Digest {
			return fmt.Errorf("the registry resolved %s to %s", rec.Digest, res.Digest)
		}
		if err := Fetch(ctx, v.o.Registry, res, q); err != nil {
			return err
		}
	}
	cfg, err := Verify(q, rec.Digest)
	if err != nil {
		return err
	}
	if rec.ConfigDigest != "" && cfg != rec.ConfigDigest {
		return fmt.Errorf("config %s is not the scanned %s: %w", cfg, rec.ConfigDigest, ErrDigestMismatch)
	}
	id, err := v.o.Loader.Load(ctx, q, rec.Digest)
	if err != nil {
		return err
	}
	if !IDMatches(id, *rec) {
		_ = v.o.Loader.Remove(ctx, id)
		return fmt.Errorf("the engine loaded %s, not the scanned %s: %w", id, ShortDigest(rec.Digest), ErrDigestMismatch)
	}
	rec.ImageID, rec.LocalName = id, LocalName(rec.Digest)
	return nil
}

// Status returns the record of a digest, or of the newest digest a ref
// resolved to.
func (v *Vetter) Status(ref string) (Record, error) {
	ref = strings.TrimSpace(ref)
	if digestRe.MatchString(ref) {
		return v.o.Store.Get(ref)
	}
	recs, err := v.o.Store.List()
	if err != nil {
		return Record{}, err
	}
	want := normaliseRef(ref)
	for _, r := range recs {
		if r.Ref == ref || normaliseRef(r.Ref) == want {
			return r, nil
		}
	}
	return Record{}, ErrNoRecord
}

// List returns every record, newest first.
func (v *Vetter) List() ([]Record, error) { return v.o.Store.List() }

// Pending maps a stage to the harness_image_pending refusal's data.
func Pending(rec Record) error { return pending(rec, rec.Stage) }

func normaliseRef(ref string) string {
	r, err := ParseReference(ref)
	if err != nil {
		return ref
	}
	return r.String()
}

// harnessError is the refusal for an approval error.
func harnessError(err error, rec Record) error {
	if errors.Is(err, ErrNotPending) {
		return &harness.Error{Code: harness.CodeImageNotPending, Msg: err.Error(),
			ImageData: map[string]any{"digest": rec.Digest, "state": string(rec.State)}}
	}
	return err
}

// ApproveError turns an Approve error into a harness refusal where it is one.
func ApproveError(err error, rec Record) error { return harnessError(err, rec) }
