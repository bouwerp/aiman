package images

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/harness"
)

// Vetter defaults.
const (
	DefaultMaxSize     = 4 << 30
	DefaultScanTimeout = 30 * time.Minute
	// PendingRetry is the retry_after_ms of harness_image_pending.
	PendingRetry = 15 * time.Second
	// gatePoll is how often a queued vetting asks the pressure gate.
	gatePoll = 5 * time.Second
)

// Notifier hears what the vetter decided. Serve adds gates, steers notes,
// emits harness_image and writes the ledger.
type Notifier interface {
	// Decided follows a finished vetting: pending_approval, blocked, failed,
	// or approved at once.
	Decided(rec Record)
	// Joined is a new session asking for a digest that already waits for
	// approval; it gets its own gate.
	Joined(rec Record, session string)
	// Approved follows an approval and the load.
	Approved(rec Record)
	// Stale follows a re-scan that found new critical CVEs or MAL- ids.
	Stale(rec Record, newIDs []string)
}

// VetterOptions configure a Vetter.
type VetterOptions struct {
	// Dir is ~/.aiman/harness.
	Dir      string
	Store    *Store
	Registry Registry
	Scanner  Scanner
	Loader   Loader
	// Reviewer is the model review (harness.images.review); nil runs no
	// review and leaves the verdict empty, which never counts as clean for a
	// session's run.
	Reviewer Reviewer
	Notify   Notifier
	// AllowedImages is harness.allowed_images: a pre-approval list.
	AllowedImages []string
	// CVEBlock is critical (default) or high.
	CVEBlock string
	// MaxSize caps the layer sum of an image; ScanTimeout a whole vetting.
	MaxSize     int64
	ScanTimeout time.Duration
	Platform    Platform
	// MayStart is the pressure gate; nil is always open.
	MayStart func() bool
	// DBFresh refuses a vetting while the scanner databases are not fresh;
	// nil reads <Dir>/db/meta.json.
	DBFresh func() error
	Now     func() time.Time
}

// Vetter resolves refs to digests and vets unknown digests one at a time.
type Vetter struct {
	o VetterOptions

	mu      sync.Mutex
	queue   []job
	queued  map[string]bool
	wake    chan struct{}
	digests sync.Map // digest -> *sync.Mutex for approvals
	wg      sync.WaitGroup
}

// job is one digest to vet, or an approved digest to fetch and load again.
type job struct {
	res    Resolved
	reload bool
}

// NewVetter checks o and fills its defaults.
func NewVetter(o VetterOptions) (*Vetter, error) {
	if o.Dir == "" || o.Store == nil || o.Registry == nil || o.Scanner == nil || o.Loader == nil || o.Notify == nil {
		return nil, errors.New("image vetter: Dir, Store, Registry, Scanner, Loader and Notify are required")
	}
	if o.AllowedImages == nil {
		o.AllowedImages = harness.DefaultAllowedImages
	}
	if o.CVEBlock != BlockHigh {
		o.CVEBlock = BlockCritical
	}
	if o.MaxSize <= 0 {
		o.MaxSize = DefaultMaxSize
	}
	if o.ScanTimeout <= 0 {
		o.ScanTimeout = DefaultScanTimeout
	}
	if o.Platform.OS == "" {
		o.Platform = HostPlatform()
	}
	if o.MayStart == nil {
		o.MayStart = func() bool { return true }
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.DBFresh == nil {
		dbDir := filepath.Join(o.Dir, "db")
		o.DBFresh = func() error {
			meta, err := ReadDBMeta(dbDir)
			if err != nil {
				return err
			}
			return meta.CheckFresh(o.Now())
		}
	}
	for _, d := range []string{"quarantine", "reports", "sbom", "scan"} {
		if err := os.MkdirAll(filepath.Join(o.Dir, d), 0o700); err != nil {
			return nil, err
		}
	}
	return &Vetter{o: o, queued: map[string]bool{}, wake: make(chan struct{}, 1)}, nil
}

// QuarantineDir is the OCI layout of digest.
func (v *Vetter) QuarantineDir(digest string) string {
	return filepath.Join(v.o.Dir, "quarantine", strings.TrimPrefix(digest, "sha256:"))
}

// ReportPath is the scan report of digest.
func (v *Vetter) ReportPath(digest string) string {
	return filepath.Join(v.o.Dir, "reports", strings.TrimPrefix(digest, "sha256:")+".md")
}

// SBOMPath is the stored CycloneDX SBOM of digest.
func (v *Vetter) SBOMPath(digest string) string {
	return filepath.Join(v.o.Dir, "sbom", strings.TrimPrefix(digest, "sha256:")+".cdx.json")
}

// Ensure answers the engine's ID of an approved image, or a harness refusal:
// pending while the digest is vetted or waits for approval, blocked for a
// blocked, failed or stale digest. An unknown digest starts vetting; a
// second caller joins it. It never waits for a scan.
func (v *Vetter) Ensure(ctx context.Context, req harness.ImageRequest) (string, error) {
	res, err := v.o.Registry.Resolve(ctx, req.Ref, v.o.Platform)
	if err != nil {
		return "", &harness.Error{Code: harness.CodeImageResolveFailed, Msg: fmt.Sprintf("resolve image %s: %v", req.Ref, err),
			ImageData: map[string]any{"ref": req.Ref}}
	}
	q := Requester{Session: req.Session, Operator: req.Operator}
	allowed := harness.ImageAllowed(req.Ref, v.o.AllowedImages)
	var start, joined bool
	var by string
	rec, err := v.o.Store.Update(res.Digest, func(r *Record, ok bool) error {
		if !ok || r.State == "" || (r.State == StateFailed && r.Retryable) {
			if res.Size() > v.o.MaxSize {
				return errTooLarge
			}
			*r = Record{Ref: req.Ref, Repo: res.Ref.Registry + "/" + res.Ref.Repo, IndexDigest: res.IndexDigest,
				ConfigDigest: res.Config.Digest, Platform: res.Platform, Size: res.Size(),
				State: StateVetting, Stage: StageQueued, Allowed: allowed, Builtin: req.Builtin, At: v.o.Now()}
			r.addRequester(q)
			start = true
			return nil
		}
		r.Allowed = r.Allowed || allowed
		r.Builtin = r.Builtin || req.Builtin
		added := r.addRequester(q)
		switch r.State {
		case StateVetting:
			return nil
		case StatePendingApproval:
			by = approveAtOnce(r.Scan, v.o.CVEBlock, req)
			// aiman's own image approves the digest rather than gate it.
			joined = added && q.Session != "" && by != ApprovedByBuiltin
			return nil
		}
		return errSkip
	})
	if errors.Is(err, errTooLarge) {
		return "", &harness.Error{Code: harness.CodeImageTooLarge,
			Msg:       fmt.Sprintf("image %s is %d bytes compressed, over harness.images.max_size %d", req.Ref, res.Size(), v.o.MaxSize),
			ImageData: map[string]any{"ref": req.Ref, "digest": res.Digest, "size": res.Size(), "limit": v.o.MaxSize}}
	}
	if err != nil {
		return "", err
	}
	switch {
	case start:
		v.enqueue(job{res: res})
	case joined:
		v.o.Notify.Joined(rec, q.Session)
	case by != "":
		v.goApprove(ApproveRequest{Digest: rec.Digest, By: by, Approver: approverOf(by)})
	}
	return v.answer(ctx, rec, res)
}

var errTooLarge = errors.New("too large")

// approveAtOnce is how a new request approves a digest that waits for
// approval: builtin for aiman's own pinned image, operator_run for the
// operator's run, or "" when it still waits.
func approveAtOnce(scan Scan, cveBlock string, req harness.ImageRequest) string {
	if !req.Builtin && !req.Operator {
		return ""
	}
	d := Decide(scan, DecideInput{CVEBlock: cveBlock, Builtin: req.Builtin, Operator: req.Operator})
	if d.State != StateApproved {
		return ""
	}
	return d.ApprovedBy
}

// approverOf names who approved at once.
func approverOf(by string) string {
	switch by {
	case ApprovedByOperatorRun:
		return "operator"
	case ApprovedByBuiltin:
		return "aiman"
	}
	return "allowed_images"
}

// answer turns a record into the image ID or a refusal.
func (v *Vetter) answer(ctx context.Context, rec Record, res Resolved) (string, error) {
	switch rec.State {
	case StateApproved:
		id, err := v.o.Loader.Present(ctx, rec.ImageID)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, ErrImageGone) {
			return "", err
		}
		// Approved, but the engine lost it: fetch the same digest and load it
		// again; the approval stands because the digest names the bytes.
		v.enqueue(job{res: res, reload: true})
		return "", pending(rec, StageFetch)
	case StateVetting:
		return "", pending(rec, rec.Stage)
	case StatePendingApproval:
		return "", pending(rec, StageAwaitingApproval)
	default:
		return "", blocked(rec)
	}
}

func pending(rec Record, stage Stage) error {
	return &harness.Error{Code: harness.CodeImagePending,
		Msg: fmt.Sprintf("image %s (%s) is %s: %s", rec.Ref, ShortDigest(rec.Digest), strings.ReplaceAll(string(rec.State), "_", " "), stage),
		ImageData: map[string]any{"digest": rec.Digest, "ref": rec.Ref, "stage": string(stage),
			"retry_after_ms": PendingRetry.Milliseconds()}}
}

func blocked(rec Record) error {
	return &harness.Error{Code: harness.CodeImageBlocked,
		Msg: fmt.Sprintf("image %s (%s) is %s: %s; only the operator can override it from the laptop", rec.Ref, ShortDigest(rec.Digest), rec.State, strings.Join(rec.Reasons, "; ")),
		ImageData: map[string]any{"digest": rec.Digest, "ref": rec.Ref, "state": string(rec.State), "reasons": nonNil(rec.Reasons),
			"critical": nonNil(rec.Scan.Critical), "static_hits": nonNil(rec.Scan.StaticHits), "verdict": rec.Scan.ModelVerdict}}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (v *Vetter) enqueue(j job) {
	v.mu.Lock()
	if v.queued[j.res.Digest] {
		v.mu.Unlock()
		return
	}
	v.queued[j.res.Digest] = true
	v.queue = append(v.queue, j)
	v.mu.Unlock()
	select {
	case v.wake <- struct{}{}:
	default:
	}
}

// Resume queues again the digests a previous serve left in vetting.
func (v *Vetter) Resume(ctx context.Context) {
	recs, err := v.o.Store.List()
	if err != nil {
		log.Printf("harness images: resume: %v", err)
		return
	}
	for i := len(recs) - 1; i >= 0; i-- {
		r := recs[i]
		if r.State != StateVetting {
			continue
		}
		res, err := v.o.Registry.Resolve(ctx, r.Repo+"@"+r.Digest, v.o.Platform)
		if err != nil {
			v.fail(r.Digest, fmt.Sprintf("resolve again after a restart: %v", err))
			continue
		}
		v.enqueue(job{res: res})
	}
}

// Run vets queued digests until ctx ends.
func (v *Vetter) Run(ctx context.Context) {
	for {
		v.Drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-v.wake:
		}
	}
}

// Drain vets the queued digests one at a time, in arrival order, each only
// while the pressure gate is open, and returns when the queue is empty or
// ctx ends.
func (v *Vetter) Drain(ctx context.Context) {
	for {
		if v.empty() {
			return
		}
		for !v.o.MayStart() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(gatePoll):
			}
		}
		j, ok := v.next()
		if !ok {
			return
		}
		if j.reload {
			v.reload(ctx, j.res)
		} else {
			v.vet(ctx, j.res)
		}
		v.mu.Lock()
		delete(v.queued, j.res.Digest)
		v.mu.Unlock()
	}
}

func (v *Vetter) empty() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.queue) == 0
}

func (v *Vetter) next() (job, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.queue) == 0 {
		return job{}, false
	}
	j := v.queue[0]
	v.queue = v.queue[1:]
	return j, true
}

// Wait waits for approvals started in the background.
func (v *Vetter) Wait() { v.wg.Wait() }

func (v *Vetter) setStage(digest string, st Stage) {
	if _, err := v.o.Store.Update(digest, func(r *Record, _ bool) error {
		r.Stage = st
		return nil
	}); err != nil {
		log.Printf("harness images: %s stage %s: %v", ShortDigest(digest), st, err)
	}
}

// fail ends a vetting as failed and removes its quarantine.
func (v *Vetter) fail(digest, reason string) { v.failWith(digest, reason, false) }

// failWith ends a vetting as failed. A retryable failure is the host's
// (scanner databases not fresh, no scanner image), not the image's: the next
// caller that asks for the digest starts its vetting again.
func (v *Vetter) failWith(digest, reason string, retryable bool) {
	_ = os.RemoveAll(v.QuarantineDir(digest))
	rec, err := v.o.Store.Update(digest, func(r *Record, _ bool) error {
		r.State, r.Stage, r.Reasons, r.At = StateFailed, "", []string{reason}, v.o.Now()
		r.Retryable = retryable
		return nil
	})
	if err != nil {
		log.Printf("harness images: %s failed (%s): %v", ShortDigest(digest), reason, err)
		return
	}
	v.o.Notify.Decided(rec)
}

// vet runs one vetting: fetch and check, CVE scan, static scan, review,
// decision. Anything that cannot finish ends as failed, never clean.
func (v *Vetter) vet(ctx context.Context, res Resolved) {
	ctx, cancel := context.WithTimeout(ctx, v.o.ScanTimeout)
	defer cancel()
	d := res.Digest
	defer func() { _ = os.RemoveAll(filepath.Join(v.o.Dir, "scan", ShortDigest(d))) }()
	if err := v.o.DBFresh(); err != nil {
		v.failWith(d, err.Error(), true)
		return
	}
	v.setStage(d, StageFetch)
	q := v.QuarantineDir(d)
	if err := Fetch(ctx, v.o.Registry, res, q); err != nil {
		v.fail(d, "fetch: "+err.Error())
		return
	}
	scan, sig, err := v.scan(ctx, res, q)
	if err != nil {
		v.failWith(d, err.Error(), errors.Is(err, ErrScannerUnavailable))
		return
	}
	rec, err := v.o.Store.Get(d)
	if err != nil {
		v.fail(d, err.Error())
		return
	}
	rec.Scan, rec.Signature = scan, sig
	// A builtin image is pinned by aiman's release; its scans still decide,
	// but no model review is needed.
	if v.o.Reviewer != nil && !rec.Builtin {
		v.setStage(d, StageReview)
		res, err := v.o.Reviewer.Review(ctx, rec, q)
		if err != nil {
			// No usable report is never clean: the vetting fails. A host that
			// cannot run a review fails retryably.
			v.failWith(d, "image review: "+err.Error(), errors.Is(err, ErrReviewUnavailable))
			return
		}
		independent := res.Independent
		scan.ModelVerdict, scan.ReviewAgent, scan.ReviewModel = res.Verdict, res.Agent, res.Model
		scan.Independent, scan.ReviewReport = &independent, res.Report
		scan.ReviewFindings = [3]int{res.High, res.Medium, res.Low}
	}
	scan.Report = v.ReportPath(d)
	rec.Scan = scan
	if err := writeReport(scan.Report, rec); err != nil {
		v.fail(d, "write the report: "+err.Error())
		return
	}
	v.decide(ctx, rec)
}

// scan runs the CVE and static stages and keeps the SBOM.
func (v *Vetter) scan(ctx context.Context, res Resolved, q string) (Scan, string, error) {
	d := res.Digest
	sig := v.signature(ctx, res)
	limit := 4 * v.o.MaxSize
	v.setStage(d, StageCVE)
	cve, err := v.o.Scanner.Scan(ctx, ScanRequest{Digest: d, Input: q, Stage: ScanStageCVE, Tools: []string{ToolTrivy}, MaxBytes: limit, Timeout: v.remaining(ctx)})
	if err != nil {
		return Scan{}, sig, fmt.Errorf("cve scan: %w", err)
	}
	v.setStage(d, StageStatic)
	static, err := v.o.Scanner.Scan(ctx, ScanRequest{Digest: d, Input: q, Stage: ScanStageStatic, Tools: []string{ToolClamAV, ToolYARA, ToolOSV}, MaxBytes: limit, Timeout: v.remaining(ctx)})
	if err != nil {
		return Scan{}, sig, fmt.Errorf("static scan: %w", err)
	}
	sbom := filepath.Join(v.o.Dir, "scan", ShortDigest(d), "out", "sbom.cdx.json")
	if b, err := os.ReadFile(sbom); err == nil { //nolint:gosec // G304: the SBOM the scan wrote
		if err := os.WriteFile(v.SBOMPath(d), b, 0o600); err != nil {
			return Scan{}, sig, err
		}
	}
	return Scan{TrivyDB: cve.TrivyDB, Critical: cve.Critical, High: cve.High, StaticHits: static.StaticHits,
		MalIDs: static.MalIDs, SBOM: v.SBOMPath(d)}, sig, nil
}

func (v *Vetter) remaining(ctx context.Context) time.Duration {
	if dl, ok := ctx.Deadline(); ok {
		return time.Until(dl)
	}
	return v.o.ScanTimeout
}

// signature looks for a cosign signature of the platform manifest or of the
// index the ref resolved through. It is shown, never decisive.
func (v *Vetter) signature(ctx context.Context, res Resolved) string {
	status := SignatureAbsent
	for _, d := range []string{res.Digest, res.IndexDigest} {
		if d == "" {
			continue
		}
		ok, err := v.o.Registry.Signed(ctx, res.Ref, d)
		switch {
		case err != nil:
			status = SignatureUnknown
		case ok:
			return SignaturePresent
		}
	}
	return status
}

// decide records the decision, loads an image approved at once, and tells
// the notifier.
func (v *Vetter) decide(ctx context.Context, rec Record) {
	dec := Decide(rec.Scan, DecideInput{CVEBlock: v.o.CVEBlock, Allowed: rec.Allowed, Operator: rec.Operator(), Builtin: rec.Builtin})
	saved, err := v.o.Store.Update(rec.Digest, func(r *Record, _ bool) error {
		r.Scan, r.Signature, r.At = rec.Scan, rec.Signature, v.o.Now()
		r.Reasons = dec.Reasons
		switch dec.State {
		case StateApproved:
			r.State, r.Stage = StatePendingApproval, StageLoad
		case StatePendingApproval:
			r.State, r.Stage = StatePendingApproval, StageAwaitingApproval
		default:
			r.State, r.Stage = dec.State, ""
		}
		return nil
	})
	if err != nil {
		v.fail(rec.Digest, err.Error())
		return
	}
	if dec.State == StateBlocked {
		_ = os.RemoveAll(v.QuarantineDir(rec.Digest))
	}
	if dec.State != StateApproved {
		v.o.Notify.Decided(saved)
		return
	}
	approved, err := v.approve(ctx, saved, ApproveRequest{Digest: rec.Digest, By: dec.ApprovedBy, Approver: approverOf(dec.ApprovedBy)})
	if err != nil {
		return // approve recorded the failure
	}
	v.o.Notify.Decided(approved)
}

// reload fetches an approved digest the engine lost and loads it again.
func (v *Vetter) reload(ctx context.Context, res Resolved) {
	ctx, cancel := context.WithTimeout(ctx, v.o.ScanTimeout)
	defer cancel()
	rec, err := v.o.Store.Get(res.Digest)
	if err != nil || rec.State != StateApproved {
		return
	}
	if err := v.loadVerified(ctx, &rec, res); err != nil {
		log.Printf("harness images: reload %s: %v", ShortDigest(res.Digest), err)
		v.fail(res.Digest, "load again: "+err.Error())
		return
	}
	if _, err := v.o.Store.Update(rec.Digest, func(r *Record, _ bool) error {
		r.ImageID, r.LocalName = rec.ImageID, rec.LocalName
		return nil
	}); err != nil {
		log.Printf("harness images: reload %s: %v", ShortDigest(res.Digest), err)
	}
	_ = os.RemoveAll(v.QuarantineDir(res.Digest))
}
