package images_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/images"
	"github.com/bouwerp/aiman/internal/harness/images/imagestest"
)

type vetFixture struct {
	v       *images.Vetter
	store   *images.Store
	reg     *imagestest.Registry
	scanner *imagestest.Scanner
	loader  *imagestest.Loader
	notes   *imagestest.Notifier
	dir     string
}

func newVetFixture(t *testing.T, mod func(*images.VetterOptions)) *vetFixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "harness")
	store, err := images.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	f := &vetFixture{store: store, reg: imagestest.NewRegistry(), scanner: &imagestest.Scanner{},
		loader: imagestest.NewLoader(), notes: &imagestest.Notifier{}, dir: dir}
	o := images.VetterOptions{
		Dir: dir, Store: store, Registry: f.reg, Scanner: f.scanner, Loader: f.loader, Notify: f.notes.Funcs(),
		DBFresh: func() error { return nil },
	}
	if mod != nil {
		mod(&o)
	}
	if f.v, err = images.NewVetter(o); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.v.Wait)
	return f
}

// push tags ref at a fresh one-layer image and returns it.
func (f *vetFixture) push(ref, salt string) imagestest.Image {
	img := imagestest.NewImage(salt, imagestest.Layer(map[string]string{"etc/" + salt: salt}))
	f.reg.Tag(ref, img)
	return img
}

// ensure asks for ref and vets whatever it queued.
func (f *vetFixture) ensure(t *testing.T, ref, session string, operator bool) (string, error) {
	t.Helper()
	id, err := f.v.Ensure(context.Background(), harness.ImageRequest{Ref: ref, Session: session, Operator: operator})
	f.v.Drain(context.Background())
	f.v.Wait()
	return id, err
}

func (f *vetFixture) record(t *testing.T, img imagestest.Image) images.Record {
	t.Helper()
	r, err := f.store.Get(img.Digest())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func code(err error) string {
	var he *harness.Error
	if errors.As(err, &he) {
		return he.Code
	}
	return ""
}

func TestQuarantineDigestMismatchFails(t *testing.T) {
	f := newVetFixture(t, nil)
	img := f.push("redis:7", "a")
	f.reg.Corrupt = img.ConfigDigest()
	if _, err := f.ensure(t, "redis:7", "s1", false); code(err) != harness.CodeImagePending {
		t.Fatalf("first ask = %v", err)
	}
	r := f.record(t, img)
	if r.State != images.StateFailed || len(r.Reasons) == 0 {
		t.Fatalf("record = %+v, want failed with a reason", r)
	}
	if _, err := os.Stat(f.v.QuarantineDir(img.Digest())); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the quarantine stayed after a digest mismatch")
	}
	if len(f.scanner.Calls) != 0 || f.loader.Loads != 0 {
		t.Fatalf("scanned %d times, loaded %d times after a mismatch", len(f.scanner.Calls), f.loader.Loads)
	}
	if _, err := f.ensure(t, "redis:7", "s1", false); code(err) != harness.CodeImageBlocked {
		t.Fatalf("ask after failure = %v, want harness_image_blocked", err)
	}
}

func TestTooLargeRefusedBeforeFetch(t *testing.T) {
	f := newVetFixture(t, func(o *images.VetterOptions) { o.MaxSize = 10 })
	img := f.push("redis:7", "a")
	_, err := f.ensure(t, "redis:7", "s1", false)
	var he *harness.Error
	if !errors.As(err, &he) || he.Code != harness.CodeImageTooLarge || he.Data()["limit"] != int64(10) {
		t.Fatalf("Ensure = %v, want harness_image_too_large with the limit", err)
	}
	if f.reg.BlobGets != 0 {
		t.Fatalf("fetched %d blobs of a refused image", f.reg.BlobGets)
	}
	if _, err := f.store.Get(img.Digest()); !errors.Is(err, images.ErrNoRecord) {
		t.Fatal("a refused image got a record")
	}
}

func TestCriticalBlocks(t *testing.T) {
	f := newVetFixture(t, nil)
	f.scanner.Set(images.ScanStageCVE, images.StageResult{Critical: []string{"CVE-2026-1"}, High: []string{"CVE-2026-2"}})
	img := f.push("postgres:16", "a")
	_, _ = f.ensure(t, "postgres:16", "", true)
	r := f.record(t, img)
	if r.State != images.StateBlocked || f.loader.Loads != 0 {
		t.Fatalf("record = %+v, loads %d; want blocked and never loaded", r, f.loader.Loads)
	}
	if len(f.notes.Decided) != 1 || f.notes.Decided[0].State != images.StateBlocked {
		t.Fatalf("notifier heard %+v", f.notes.Decided)
	}
	_, err := f.ensure(t, "postgres:16", "s1", false)
	var he *harness.Error
	if !errors.As(err, &he) || he.Code != harness.CodeImageBlocked || !slices.Equal(he.Data()["critical"].([]string), []string{"CVE-2026-1"}) {
		t.Fatalf("next ask = %v", err)
	}
	if _, err := os.Stat(f.v.QuarantineDir(img.Digest())); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a blocked image's quarantine stayed")
	}
}

func TestMALIdBlocks(t *testing.T) {
	f := newVetFixture(t, nil)
	f.scanner.Set(images.ScanStageStatic, images.StageResult{MalIDs: []string{"MAL-2026-9"}})
	img := f.push("node:22", "a")
	_, _ = f.ensure(t, "node:22", "s1", false)
	if r := f.record(t, img); r.State != images.StateBlocked {
		t.Fatalf("state = %s, want blocked", r.State)
	}
}

func TestHighsNeedApproval(t *testing.T) {
	f := newVetFixture(t, func(o *images.VetterOptions) { o.Reviewer = imagestest.Reviewer{Verdict: images.VerdictClean} })
	f.scanner.Set(images.ScanStageCVE, images.StageResult{High: []string{"CVE-2026-5"}})
	img := f.push("postgres:16", "a")
	_, err := f.ensure(t, "postgres:16", "", true)
	r := f.record(t, img)
	if r.State != images.StatePendingApproval || f.loader.Loads != 0 {
		t.Fatalf("record = %+v; a high asks even for a listed image on the operator's run", r)
	}
	if code(err) != harness.CodeImagePending {
		t.Fatalf("Ensure = %v", err)
	}
	if _, err := f.ensure(t, "postgres:16", "", true); code(err) != harness.CodeImagePending {
		t.Fatalf("operator ask of a pending image with highs = %v; it must not approve itself", err)
	}
}

func TestApprovalRecordsAcceptedHighs(t *testing.T) {
	f := newVetFixture(t, nil)
	f.scanner.Set(images.ScanStageCVE, images.StageResult{High: []string{"CVE-2026-5", "CVE-2026-6"}})
	img := f.push("redis:7", "a")
	_, _ = f.ensure(t, "redis:7", "s1", false)
	rec, err := f.v.Approve(context.Background(), images.ApproveRequest{Digest: img.Digest(), By: images.ApprovedBySession, Approver: "s1", Quote: "ok, accept both highs"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != images.StateApproved || !slices.Equal(rec.AcceptedHighs, []string{"CVE-2026-5", "CVE-2026-6"}) || rec.Quote == "" {
		t.Fatalf("approved record = %+v", rec)
	}
	if rec.ImageID != img.Digest() || f.loader.Loads != 1 {
		t.Fatalf("image id %s after %d loads", rec.ImageID, f.loader.Loads)
	}
	if _, err := os.Stat(f.v.QuarantineDir(img.Digest())); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the quarantine stayed after approval")
	}
	if len(f.notes.Approved) != 1 {
		t.Fatalf("notifier heard %d approvals", len(f.notes.Approved))
	}
	id, err := f.ensure(t, "redis:7", "s2", false)
	if err != nil || id != img.Digest() {
		t.Fatalf("Ensure after approval = %s, %v", id, err)
	}
}

func TestLoadedImageIDMismatchFails(t *testing.T) {
	f := newVetFixture(t, nil)
	img := f.push("redis:7", "a")
	_, _ = f.ensure(t, "redis:7", "s1", false)
	f.loader.LoadID = "sha256:" + "ab" + img.Digest()[9:]
	if _, err := f.v.Approve(context.Background(), images.ApproveRequest{Digest: img.Digest(), By: images.ApprovedByOperator, Approver: "operator"}); !errors.Is(err, images.ErrDigestMismatch) {
		t.Fatalf("Approve = %v, want a digest mismatch", err)
	}
	if r := f.record(t, img); r.State != images.StateFailed {
		t.Fatalf("state = %s, want failed", r.State)
	}
	if _, err := f.loader.Present(context.Background(), f.loader.LoadID); err == nil {
		t.Fatal("the mismatched image stayed in the engine")
	}
}

func TestNewDigestVettedAgain(t *testing.T) {
	f := newVetFixture(t, nil)
	first := f.push("redis:7", "a")
	_, _ = f.ensure(t, "redis:7", "s1", false)
	if _, err := f.v.Approve(context.Background(), images.ApproveRequest{Digest: first.Digest(), By: images.ApprovedByOperator, Approver: "operator"}); err != nil {
		t.Fatal(err)
	}
	moved := f.push("redis:7", "b")
	_, err := f.ensure(t, "redis:7", "s1", false)
	if code(err) != harness.CodeImagePending {
		t.Fatalf("Ensure of a moved tag = %v, want pending", err)
	}
	if r := f.record(t, moved); r.State != images.StatePendingApproval {
		t.Fatalf("new digest state = %s", r.State)
	}
	if r := f.record(t, first); r.State != images.StateApproved {
		t.Fatalf("old digest state = %s", r.State)
	}
}

func TestRescanMarksStale(t *testing.T) {
	f := newVetFixture(t, nil)
	f.scanner.Set(images.ScanStageCVE, images.StageResult{High: []string{"CVE-1"}})
	img := f.push("redis:7", "a")
	_, _ = f.ensure(t, "redis:7", "s1", false)
	if _, err := f.v.Approve(context.Background(), images.ApproveRequest{Digest: img.Digest(), By: images.ApprovedByOperator, Approver: "operator"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.v.SBOMPath(img.Digest()), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	// New highs only update the record.
	f.scanner.Set(images.ScanStageSBOM, images.StageResult{High: []string{"CVE-1", "CVE-3"}})
	f.v.Rescan(context.Background())
	if r := f.record(t, img); r.State != images.StateApproved || !slices.Contains(r.Scan.High, "CVE-3") || r.RescannedAt.IsZero() {
		t.Fatalf("after a new high: %+v", r)
	}
	f.scanner.Set(images.ScanStageSBOM, images.StageResult{Critical: []string{"CVE-9"}, High: []string{"CVE-1"}})
	f.v.Rescan(context.Background())
	r := f.record(t, img)
	if r.State != images.StateStale || len(f.notes.Stale) != 1 {
		t.Fatalf("after a new critical: %+v, %d stale notes", r, len(f.notes.Stale))
	}
	if _, err := f.loader.Present(context.Background(), r.ImageID); err != nil {
		t.Fatal("a re-scan removed the image")
	}
}

func TestStaleBlocksNextUp(t *testing.T) {
	f := newVetFixture(t, nil)
	img := f.push("redis:7", "a")
	_, _ = f.ensure(t, "redis:7", "s1", false)
	if _, err := f.v.Approve(context.Background(), images.ApproveRequest{Digest: img.Digest(), By: images.ApprovedByOperator, Approver: "operator"}); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(f.v.SBOMPath(img.Digest()), []byte("{}"), 0o600)
	f.scanner.Set(images.ScanStageSBOM, images.StageResult{MalIDs: []string{"MAL-2026-1"}})
	f.v.Rescan(context.Background())
	_, err := f.ensure(t, "redis:7", "s1", false)
	var he *harness.Error
	if !errors.As(err, &he) || he.Code != harness.CodeImageBlocked || he.Data()["state"] != "stale" {
		t.Fatalf("Ensure of a stale digest = %v", err)
	}
	// The operator's accept-risk approves it again without a new load.
	loads := f.loader.Loads
	rec, err := f.v.Approve(context.Background(), images.ApproveRequest{Digest: img.Digest(), By: images.ApprovedByOperator, Approver: "operator", AcceptRisk: true, Reason: "pinned, isolated"})
	if err != nil || rec.State != images.StateApproved || !rec.AcceptRisk || f.loader.Loads != loads {
		t.Fatalf("accept-risk = %+v, %v (loads %d -> %d)", rec, err, loads, f.loader.Loads)
	}
}

func TestOperatorRunVetted(t *testing.T) {
	f := newVetFixture(t, nil)
	img := f.push("ghcr.io/o/tool:1", "a")
	if _, err := f.ensure(t, "ghcr.io/o/tool:1", "s1", true); code(err) != harness.CodeImagePending {
		t.Fatalf("first ask = %v", err)
	}
	if len(f.scanner.Calls) != 2 {
		t.Fatalf("the operator's image ran %d scan stages, want cve and static", len(f.scanner.Calls))
	}
	r := f.record(t, img)
	if r.State != images.StateApproved || r.ApprovedBy != images.ApprovedByOperatorRun {
		t.Fatalf("record = %+v, want approved by operator_run", r)
	}
	if id, err := f.ensure(t, "ghcr.io/o/tool:1", "s1", true); err != nil || id != img.Digest() {
		t.Fatalf("next ask = %s, %v", id, err)
	}
}

func TestAllowedImageCleanAutoApproved(t *testing.T) {
	f := newVetFixture(t, func(o *images.VetterOptions) { o.Reviewer = imagestest.Reviewer{Verdict: images.VerdictClean} })
	img := f.push("postgres:16", "a")
	_, _ = f.ensure(t, "postgres:16", "s1", false)
	if r := f.record(t, img); r.State != images.StateApproved || r.ApprovedBy != images.ApprovedByConfig {
		t.Fatalf("record = %+v, want approved by config", r)
	}
	// Without a review verdict a session's listed image still asks.
	g := newVetFixture(t, nil)
	img2 := g.push("postgres:16", "b")
	_, _ = g.ensure(t, "postgres:16", "s1", false)
	if r := g.record(t, img2); r.State != images.StatePendingApproval {
		t.Fatalf("no verdict: state = %s, want pending_approval", r.State)
	}
	// An unlisted image asks even when clean.
	img3 := f.push("redis:7", "c")
	_, _ = f.ensure(t, "redis:7", "s1", false)
	if r := f.record(t, img3); r.State != images.StatePendingApproval {
		t.Fatalf("unlisted: state = %s", r.State)
	}
}

func TestAllowedImageWithHighAsks(t *testing.T) {
	f := newVetFixture(t, func(o *images.VetterOptions) { o.Reviewer = imagestest.Reviewer{Verdict: images.VerdictClean} })
	f.scanner.Set(images.ScanStageCVE, images.StageResult{High: []string{"CVE-2026-7"}})
	img := f.push("golang:1.25", "a")
	_, _ = f.ensure(t, "golang:1.25", "s1", false)
	if r := f.record(t, img); r.State != images.StatePendingApproval {
		t.Fatalf("state = %s, want pending_approval", r.State)
	}
}

func TestAcceptRiskOnlyForBlocked(t *testing.T) {
	f := newVetFixture(t, nil)
	f.scanner.Set(images.ScanStageStatic, images.StageResult{StaticHits: []string{"clamav:Eicar layer 0/x"}})
	img := f.push("redis:7", "a")
	_, _ = f.ensure(t, "redis:7", "s1", false)
	if _, err := f.v.Approve(context.Background(), images.ApproveRequest{Digest: img.Digest(), By: images.ApprovedByOperator}); !errors.Is(err, images.ErrNotPending) {
		t.Fatalf("approve blocked without accept-risk = %v", err)
	}
	rec, err := f.v.Approve(context.Background(), images.ApproveRequest{Digest: img.Digest(), By: images.ApprovedByOperator, Approver: "operator", AcceptRisk: true, Reason: "test fixture"})
	if err != nil || rec.State != images.StateApproved || f.reg.BlobGets == 0 {
		t.Fatalf("accept-risk = %+v, %v", rec, err)
	}
}

func TestQueueWaitsForPressureGate(t *testing.T) {
	open := false
	f := newVetFixture(t, func(o *images.VetterOptions) { o.MayStart = func() bool { return open } })
	f.push("redis:7", "a")
	if _, err := f.v.Ensure(context.Background(), harness.ImageRequest{Ref: "redis:7", Session: "s1"}); code(err) != harness.CodeImagePending {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	f.v.Drain(ctx)
	cancel()
	if len(f.scanner.Calls) != 0 {
		t.Fatal("a vetting started while the pressure gate was closed")
	}
}

func TestStaleDatabasesFailVetting(t *testing.T) {
	fresh := images.ErrDBStale
	f := newVetFixture(t, func(o *images.VetterOptions) {
		o.DBFresh = func() error { return fresh }
	})
	img := f.push("redis:7", "a")
	_, _ = f.ensure(t, "redis:7", "s1", false)
	if r := f.record(t, img); r.State != images.StateFailed || !r.Retryable || len(f.scanner.Calls) != 0 {
		t.Fatalf("record = %+v, want a retryable failure before any scan", r)
	}
	// Once the databases are fresh, the next ask vets the digest again.
	fresh = nil
	if _, err := f.ensure(t, "redis:7", "s1", false); code(err) != harness.CodeImagePending {
		t.Fatalf("ask after the refresh = %v", err)
	}
	if r := f.record(t, img); r.State != images.StatePendingApproval {
		t.Fatalf("state after the retry = %s", r.State)
	}
}

func TestScannerFailureIsNotClean(t *testing.T) {
	f := newVetFixture(t, nil)
	f.scanner.Errs = map[string]error{images.ScanStageStatic: errors.New("static stage ran out of time")}
	img := f.push("redis:7", "a")
	_, _ = f.ensure(t, "redis:7", "s1", false)
	if r := f.record(t, img); r.State != images.StateFailed || r.Retryable {
		t.Fatalf("record = %+v, want a failure only the operator overrides", r)
	}
	if _, err := f.ensure(t, "redis:7", "s1", false); code(err) != harness.CodeImageBlocked {
		t.Fatalf("next ask = %v", err)
	}
}
