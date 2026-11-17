package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/images"
	"github.com/bouwerp/aiman/internal/harness/images/imagestest"
	"github.com/bouwerp/aiman/internal/todo"
	"gopkg.in/yaml.v3"
)

// harnessVet is an image vetter over fakes, reporting to the server.
type harnessVet struct {
	v       *images.Vetter
	reg     *imagestest.Registry
	scanner *imagestest.Scanner
	loader  *imagestest.Loader
	app     imagestest.Image
	pg      imagestest.Image
}

func newHarnessVet(t *testing.T, srv *Server) *harnessVet {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "harness")
	store, err := images.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	hv := &harnessVet{reg: imagestest.NewRegistry(), scanner: &imagestest.Scanner{}, loader: imagestest.NewLoader()}
	hv.app = imagestest.NewImage("golang", imagestest.Layer(map[string]string{"usr/local/go/VERSION": "go1.25"}))
	hv.pg = imagestest.NewImage("postgres", imagestest.Layer(map[string]string{"usr/lib/postgresql/16": "pg"}))
	hv.reg.Tag("golang:1.25", hv.app)
	hv.reg.Tag("postgres:16", hv.pg)
	hv.v, err = images.NewVetter(images.VetterOptions{
		Dir: dir, Store: store, Registry: hv.reg, Scanner: hv.scanner, Loader: hv.loader,
		Notify: srv.HarnessImageNotifier(), DBFresh: func() error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.v.Wait(); srv.WaitHarnessImages() })
	return hv
}

// drain vets what is queued and waits for background approvals.
func (hv *harnessVet) drain(srv *Server) {
	hv.v.Drain(context.Background())
	hv.v.Wait()
	srv.WaitHarnessImages()
}

// upPending asks for the plain scenario as the session, wants the image
// pending, and vets it.
func (f *harnessFixture) upPending(t *testing.T, session string) {
	t.Helper()
	data := wantHarnessCode(t, "up of an unvetted image", rpc(t, f.srv, session, "harness.up", map[string]any{"session": session, "scenario": "plain"}), harness.CodeImagePending)
	if data["digest"] != f.vet.app.Digest() || data["retry_after_ms"] == nil || data["stage"] == "" {
		t.Fatalf("pending data = %v", data)
	}
	f.vet.drain(f.srv)
}

func listOf(t *testing.T, srv *Server, id string) domain.TodoList {
	t.Helper()
	l, err := srv.todos.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func gateItem(l domain.TodoList, digest string) (domain.TodoItem, bool) {
	for _, it := range l.Items {
		if it.Ref == todo.ImageGateRef(digest) {
			return it, true
		}
	}
	return domain.TodoItem{}, false
}

func steerTexts(t *testing.T, srv *Server, notes *fakeSteerStore, id string) string {
	t.Helper()
	settle()
	var b strings.Builder
	for _, r := range notes.records() {
		if r.SessionID == id {
			b.WriteString(r.Text + "\n")
		}
	}
	return b.String()
}

func imageRecord(t *testing.T, srv *Server, digest string) images.Record {
	t.Helper()
	var res struct {
		Type  string        `json:"type"`
		Image images.Record `json:"image"`
	}
	rpcOK(t, srv, "", "harness.image_status", map[string]any{"digest": digest}, &res)
	return res.Image
}

func TestImageGateAdded(t *testing.T) {
	f := harnessFixtureOf(t, harnessSetup{vet: true})
	f.vet.scanner.Set(images.ScanStageCVE, images.StageResult{High: []string{"CVE-2026-11"}})
	f.upPending(t, "s1")
	d := f.vet.app.Digest()
	it, ok := gateItem(listOf(t, f.srv, "s1"), d)
	if !ok || it.State != domain.TodoPending || it.Text != "Image approved: golang:1.25 ("+images.ShortDigest(d)+")" {
		t.Fatalf("gate = %+v, %v", it, ok)
	}
	note := steerTexts(t, f.srv, f.notes, "s1")
	for _, want := range []string{"needs approval", "1 high (CVE-2026-11)", "aiman harness approve-image " + d, "Report: "} {
		if !strings.Contains(note, want) {
			t.Fatalf("steering note lacks %q:\n%s", want, note)
		}
	}
	if rows := ledgerRows(t, f.srv, domain.EventHarnessImageVetted); len(rows) != 1 || rows[0]["state"] != "pending_approval" {
		t.Fatalf("harness.image_vetted = %v", rows)
	}
	// A session cannot close the gate itself.
	wantHarnessCode(t, "session closes its image gate",
		rpc(t, f.srv, "s1", "todo.update", map[string]any{"session": "s1", "id": it.ID, "state": "done"}), CodeGateNeedsOperator)
	// todo.gate refuses the serve-only prefix.
	if r := rpc(t, f.srv, "s1", "todo.gate", map[string]any{"session": "s1", "ref": todo.ImageGateRef(d)}); r.Error == nil {
		t.Fatal("todo.gate opened an image gate")
	}
	// Until it is approved, up stays pending at awaiting_approval.
	data := wantHarnessCode(t, "up while pending", rpc(t, f.srv, "s1", "harness.up", map[string]any{"session": "s1", "scenario": "plain"}), harness.CodeImagePending)
	if data["stage"] != string(images.StageAwaitingApproval) {
		t.Fatalf("stage = %v", data["stage"])
	}
}

func TestApproveImageNeedsQuote(t *testing.T) {
	f := harnessFixtureOf(t, harnessSetup{vet: true})
	f.vet.scanner.Set(images.ScanStageCVE, images.StageResult{High: []string{"CVE-2026-11"}})
	f.upPending(t, "s1")
	d := f.vet.app.Digest()
	wantHarnessCode(t, "no quote", rpc(t, f.srv, "s1", "harness.image_approve", map[string]any{"digest": d}), CodeInvalidParams)
	wantHarnessCode(t, "blank quote", rpc(t, f.srv, "s1", "harness.image_approve", map[string]any{"digest": d, "quote": "  "}), CodeInvalidParams)
	rpcOK(t, f.srv, "s1", "harness.image_approve", map[string]any{"digest": d, "quote": "yes, run it, I accept CVE-2026-11"}, nil)
	rec := imageRecord(t, f.srv, d)
	if rec.State != images.StateApproved || rec.ApprovedBy != images.ApprovedBySession || rec.Approver != "s1" || rec.Quote != "" {
		t.Fatalf("status view = %+v (the quote stays out of the reply)", rec)
	}
	rows := ledgerRows(t, f.srv, domain.EventHarnessImageApproved)
	if len(rows) != 1 || rows[0]["quote"] != "yes, run it, I accept CVE-2026-11" {
		t.Fatalf("harness.image_approved = %v", rows)
	}
	if hs, _ := rows[0]["accepted_highs"].([]any); len(hs) != 1 || hs[0] != "CVE-2026-11" {
		t.Fatalf("accepted_highs = %v", rows[0]["accepted_highs"])
	}
	if it, _ := gateItem(listOf(t, f.srv, "s1"), d); it.State != domain.TodoDone {
		t.Fatalf("gate after approval = %+v", it)
	}
}

func TestApproveImageOtherSessionRefused(t *testing.T) {
	f := harnessFixtureOf(t, harnessSetup{vet: true})
	f.upPending(t, "s1")
	d := f.vet.app.Digest()
	wantHarnessCode(t, "s2 approves s1's gate", rpc(t, f.srv, "s2", "harness.image_approve", map[string]any{"digest": d, "quote": "ok"}), CodeGateNeedsOperator)
	if rec := imageRecord(t, f.srv, d); rec.State != images.StatePendingApproval {
		t.Fatalf("state = %s", rec.State)
	}
}

func TestTUICloseApproves(t *testing.T) {
	f := harnessFixtureOf(t, harnessSetup{vet: true})
	f.upPending(t, "s1")
	d := f.vet.app.Digest()
	it, _ := gateItem(listOf(t, f.srv, "s1"), d)
	rpcOK(t, f.srv, "", "todo.update", map[string]any{"session": "s1", "id": it.ID, "state": "done"}, nil)
	f.srv.WaitHarnessImages()
	rec := imageRecord(t, f.srv, d)
	if rec.State != images.StateApproved || rec.ApprovedBy != images.ApprovedByOperator {
		t.Fatalf("after the operator closed the gate: %+v", rec)
	}
}

func TestApprovalClosesEveryGate(t *testing.T) {
	f := harnessFixtureOf(t, harnessSetup{vet: true})
	f.upPending(t, "s1")
	d := f.vet.app.Digest()
	// s2 asks for the same digest while it waits: it joins and gets a gate.
	f.upPending(t, "s2")
	if _, ok := gateItem(listOf(t, f.srv, "s2"), d); !ok {
		t.Fatal("the joining session got no gate")
	}
	rpcOK(t, f.srv, "", "harness.image_approve", map[string]any{"digest": d}, nil)
	for _, id := range []string{"s1", "s2"} {
		if it, _ := gateItem(listOf(t, f.srv, id), d); it.State != domain.TodoDone {
			t.Fatalf("%s gate after approval = %+v", id, it)
		}
		if note := steerTexts(t, f.srv, f.notes, id); !strings.Contains(note, "approved by operator") {
			t.Fatalf("%s got no approval note:\n%s", id, note)
		}
	}
}

func TestAcceptRiskNeedsOperator(t *testing.T) {
	f := harnessFixtureOf(t, harnessSetup{vet: true})
	f.vet.scanner.Set(images.ScanStageStatic, images.StageResult{StaticHits: []string{"yara:SUSP_Miner layer 0/usr/bin/x"}})
	wantHarnessCode(t, "up", rpc(t, f.srv, "s1", "harness.up", map[string]any{"session": "s1", "scenario": "plain"}), harness.CodeImagePending)
	f.vet.drain(f.srv)
	d := f.vet.app.Digest()
	data := wantHarnessCode(t, "up of a blocked image", rpc(t, f.srv, "s1", "harness.up", map[string]any{"session": "s1", "scenario": "plain"}), harness.CodeImageBlocked)
	if data["state"] != "blocked" {
		t.Fatalf("blocked data = %v", data)
	}
	if _, ok := gateItem(listOf(t, f.srv, "s1"), d); ok {
		t.Fatal("a blocked image got a gate")
	}
	if note := steerTexts(t, f.srv, f.notes, "s1"); !strings.Contains(note, "is blocked") {
		t.Fatalf("no blocked note:\n%s", note)
	}
	wantHarnessCode(t, "session accept-risk", rpc(t, f.srv, "s1", "harness.image_approve", map[string]any{"digest": d, "accept_risk": true, "reason": "x", "quote": "x"}), CodeNeedsOperator)
	wantHarnessCode(t, "operator without accept-risk", rpc(t, f.srv, "", "harness.image_approve", map[string]any{"digest": d}), harness.CodeImageNotPending)
	wantHarnessCode(t, "accept-risk without reason", rpc(t, f.srv, "", "harness.image_approve", map[string]any{"digest": d, "accept_risk": true}), CodeInvalidParams)
	rpcOK(t, f.srv, "", "harness.image_approve", map[string]any{"digest": d, "accept_risk": true, "reason": "false positive in a test fixture"}, nil)
	if rec := imageRecord(t, f.srv, d); rec.State != images.StateApproved || !rec.AcceptRisk || rec.Reason != "" {
		t.Fatalf("after accept-risk: %+v", rec)
	}
	if rows := ledgerRows(t, f.srv, domain.EventHarnessImageBlocked); len(rows) != 1 {
		t.Fatalf("harness.image_blocked = %v", rows)
	}
}

func TestComposePullNever(t *testing.T) {
	f := harnessFixtureOf(t, harnessSetup{vet: true})
	wantHarnessCode(t, "up", rpc(t, f.srv, "", "harness.up", map[string]any{"session": "s1", "scenario": "pg"}), harness.CodeImagePending)
	f.vet.drain(f.srv)
	// The operator's own run approves clean images with no high at once.
	run := harnessUp(t, f.srv, "", map[string]any{"session": "s1", "scenario": "pg", "wait": true})
	if run.State != harness.StateReady {
		t.Fatalf("run = %+v", run)
	}
	if run.Images[harness.ServiceApp] != f.vet.app.Digest() || run.Images["db"] != f.vet.pg.Digest() {
		t.Fatalf("run images = %v, want the vetted digests", run.Images)
	}
	matches, _ := filepath.Glob(filepath.Join(f.srv.harness.d.Manager.RunDir(run.Session, run.ID), "*", "compose.yaml"))
	if len(matches) == 0 {
		matches, _ = filepath.Glob(filepath.Join(f.srv.harness.d.Manager.RunDir(run.Session, run.ID), "compose.yaml"))
	}
	if len(matches) != 1 {
		t.Fatalf("compose file: %v", matches)
	}
	b, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Services map[string]struct {
			Image      string `yaml:"image"`
			PullPolicy string `yaml:"pull_policy"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	for name, svc := range doc.Services {
		if svc.PullPolicy != "never" || !strings.HasPrefix(svc.Image, "sha256:") {
			t.Fatalf("service %s: image %q pull_policy %q", name, svc.Image, svc.PullPolicy)
		}
	}
	for _, want := range []string{f.vet.app.Digest(), f.vet.pg.Digest()} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("compose names no %s", want)
		}
	}
}

func TestHarnessImageStatusList(t *testing.T) {
	f := harnessFixtureOf(t, harnessSetup{vet: true})
	f.upPending(t, "s1")
	var res struct {
		Type   string          `json:"type"`
		Images json.RawMessage `json:"images"`
	}
	rpcOK(t, f.srv, "s2", "harness.image_status", nil, &res)
	var list []images.Record
	if err := json.Unmarshal(res.Images, &list); err != nil || res.Type != "harness_images" || len(list) != 1 {
		t.Fatalf("status list = %s, %v", res.Images, err)
	}
	rec := imageRecord(t, f.srv, "golang:1.25")
	if rec.Digest != f.vet.app.Digest() {
		t.Fatalf("status by ref = %+v", rec)
	}
}
