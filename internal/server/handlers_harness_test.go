package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/edge"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// harnessDocker is a Docker engine that holds images and answers every
// compose call with success.
type harnessDocker struct {
	mu     sync.Mutex
	images map[string]string
}

func (d *harnessDocker) ImageID(_ context.Context, ref string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if id, ok := d.images[ref]; ok {
		return id, nil
	}
	return "", harness.ErrImageNotFound
}

func (d *harnessDocker) Compose(_ context.Context, call harness.ComposeCall) (string, int, error) {
	if len(call.Args) > 0 && call.Args[0] == "port" {
		return "127.0.0.1:41873\n", 0, nil
	}
	return "", 0, nil
}

type harnessEdge struct{}

func (harnessEdge) Health(context.Context, string, string) (edge.Health, error) {
	return edge.Health{Relays: []edge.RelayStatus{{
		RelaySpec: edge.RelaySpec{Name: harness.RelayDAP, Kind: edge.KindDAP},
		Holder:    &edge.DAPHolder{ClientName: "vscode"},
	}}}, nil
}

func (harnessEdge) DAPDrop(context.Context, string, string, string) (*edge.DAPHolder, error) {
	return &edge.DAPHolder{ClientName: "vscode"}, nil
}

// stubDebug is the Go adapter with a fixed dlv path, so nothing is built.
type stubDebug struct{ harness.GoDebug }

func (*stubDebug) Spec(context.Context, *harness.File) (*harness.DebugSpec, error) {
	return harness.GoDebugSpec("/opt/aiman-tools/dlv"), nil
}

const serverHarnessFile = `version: 1
app:
  run: serve
  ports: [{name: web, container: 8080}]
services:
  db:
    image: postgres:16
    limits: {memory: 1g}
scenarios:
  - id: plain
    steps:
      - {do: "Open {{web}}/orders/42 and press Refund.", expect: "Refunded"}
  - id: pg
    services: [db]
`

type harnessSetup struct {
	repos     map[string]config.RepoBootstrap
	images    []string
	runMemory int64
	// vet wires image vetting with fakes; s2 then shares s1's checkout.
	vet bool
	// debug adds app.debug {lang: go} to the file and a stub Go adapter.
	debug bool
}

// harnessServer is a server with sessions s1 (on a committed checkout holding
// the harness file) and s2, and a manager over a fake Docker engine.
func harnessServer(t *testing.T, setup harnessSetup) (*Server, string) {
	t.Helper()
	f := harnessFixtureOf(t, setup)
	return f.srv, f.dir
}

// harnessFixture is a harness server with its parts.
type harnessFixture struct {
	srv   *Server
	dir   string
	notes *fakeSteerStore
	// vet is set when the setup asked for image vetting.
	vet *harnessVet
}

func harnessFixtureOf(t *testing.T, setup harnessSetup) *harnessFixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(filepath.Join(dir, ".aiman"), 0o750); err != nil {
		t.Fatal(err)
	}
	ciGit(t, dir, "init", "-q", "-b", "feat")
	ciGit(t, dir, "config", "user.name", "T")
	ciGit(t, dir, "config", "user.email", "t@example.com")
	file := serverHarnessFile
	if setup.debug {
		file = strings.Replace(file, "  run: serve\n", "  run: /out/app serve\n  debug: {lang: go}\n", 1)
	}
	if err := os.WriteFile(filepath.Join(dir, harness.FilePath), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n\ngo 1.25\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ciGit(t, dir, "add", "-A")
	ciGit(t, dir, "commit", "-q", "-m", "init")
	s1 := todoSession("s1")
	s1.WorktreePath, s1.RepoName = dir, "o/r"
	s2 := todoSession("s2")
	if setup.vet {
		s2.WorktreePath, s2.RepoName = dir, "o/r"
	}
	srv, _, notes := todoServer(t, s1, s2)
	fx := &harnessFixture{srv: srv, dir: dir, notes: notes}
	images := setup.images
	if images == nil {
		images = []string{"golang:1.25", "postgres:16", harness.EdgeImage}
	}
	d := &harnessDocker{images: map[string]string{}}
	for i, ref := range images {
		d.images[ref] = fmt.Sprintf("sha256:%064x", i+1)
	}
	var vetter harness.ImageVetter
	if setup.vet {
		fx.vet = newHarnessVet(t, srv)
		vetter = fx.vet.v
	}
	m, err := harness.NewManager(harness.Options{
		Images:   vetter,
		Dir:      filepath.Join(t.TempDir(), "harness"),
		AimanBin: "/usr/local/bin/aiman",
		UID:      1000, GID: 1000,
		Docker:    d,
		Edge:      harnessEdge{},
		RunMemory: setup.runMemory,
		AppImage:  func(string, string) (string, error) { return "golang:1.25", nil },
		Record:    srv.HarnessRecorder(),

		DebugAdapters: []harness.DebugAdapter{&stubDebug{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	deps := HarnessDeps{Manager: m, Repos: setup.repos}
	if fx.vet != nil {
		deps.Images = fx.vet.v
	}
	srv.SetHarness(deps)
	return fx
}

type harnessRunResult struct {
	Type string      `json:"type"`
	Run  harness.Run `json:"run"`
}

func harnessUp(t *testing.T, srv *Server, caller string, params map[string]any) harness.Run {
	t.Helper()
	var res harnessRunResult
	rpcOK(t, srv, caller, "harness.up", params, &res)
	if res.Type != "harness_run" {
		t.Fatalf("type = %q", res.Type)
	}
	return res.Run
}

func wantHarnessCode(t *testing.T, what string, resp Response, code string) map[string]any {
	t.Helper()
	if resp.Error == nil || resp.Error.Code != code {
		t.Fatalf("%s: want %s, got error %+v result %+v", what, code, resp.Error, resp.Result)
	}
	var data map[string]any
	if resp.Error.Data != nil {
		raw, _ := json.Marshal(resp.Error.Data)
		if err := json.Unmarshal(raw, &data); err != nil {
			t.Fatal(err)
		}
	}
	return data
}

func ledgerRows(t *testing.T, srv *Server, kind domain.EventKind) []map[string]any {
	t.Helper()
	events, err := srv.repo.(sessionEventLister).ListSessionEvents(context.Background(), "s1", 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, ev := range events {
		if ev.Kind != kind {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func TestHarnessUp(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{})
	run := harnessUp(t, srv, "", map[string]any{"session": "s1", "scenario": "plain", "wait": true})
	if run.State != harness.StateReady || run.Source != harness.SourceFile || run.Session != "s1" || run.Dirty {
		t.Fatalf("run = %+v", run)
	}
	if len(run.Ports) != 1 || run.Ports[0].Name != "web" || run.Ports[0].HostPort != 41873 {
		t.Fatalf("ports = %+v", run.Ports)
	}
	if rows := ledgerRows(t, srv, domain.EventHarnessStarted); len(rows) != 1 || rows[0]["source"] != "file" || rows[0]["run"] != run.ID {
		t.Fatalf("harness.started = %v", rows)
	}
	if rows := ledgerRows(t, srv, domain.EventHarnessReady); len(rows) != 1 {
		t.Fatalf("harness.ready = %v", rows)
	}
	// One live run per session.
	wantHarnessCode(t, "second up", rpc(t, srv, "", "harness.up", map[string]any{"session": "s1", "scenario": "plain"}), harness.CodeRunning)
	// status, result and list read it back; a session in scope may read.
	var st harnessRunResult
	rpcOK(t, srv, "s1", "harness.status", map[string]any{"session": "s1"}, &st)
	if st.Run.ID != run.ID {
		t.Fatalf("status = %+v", st.Run)
	}
	rpcOK(t, srv, "", "harness.result", map[string]any{"run": run.ID}, &st)
	if st.Type != "harness_result" || st.Run.ID != run.ID {
		t.Fatalf("result = %+v", st)
	}
	var list struct {
		Type      string        `json:"type"`
		Source    string        `json:"source"`
		Scenarios []any         `json:"scenarios"`
		Runs      []harness.Run `json:"runs"`
	}
	rpcOK(t, srv, "", "harness.list", map[string]any{"session": "s1"}, &list)
	if list.Type != "harness_list" || list.Source != "file" || len(list.Scenarios) != 2 || len(list.Runs) != 1 {
		t.Fatalf("list = %+v", list)
	}
	// Another session is out of scope.
	wantHarnessCode(t, "s2 reads s1", rpc(t, srv, "s2", "harness.status", map[string]any{"session": "s1"}), CodeSessionScope)
	wantHarnessCode(t, "s2 downs s1's run", rpc(t, srv, "s2", "harness.down", map[string]any{"run": run.ID}), CodeSessionScope)
	// The owner takes it down.
	rpcOK(t, srv, "s1", "harness.down", map[string]any{"run": run.ID}, &st)
	if st.Run.State != harness.StateStopped {
		t.Fatalf("after down: %+v", st.Run)
	}
	if rows := ledgerRows(t, srv, domain.EventHarnessStopped); len(rows) != 1 || rows[0]["reason"] != "down" {
		t.Fatalf("harness.stopped = %v", rows)
	}
}

func TestHarnessVerdictNeedsOperator(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{})
	run := harnessUp(t, srv, "s1", map[string]any{"session": "s1", "scenario": "plain", "wait": true})
	wantHarnessCode(t, "session verdict", rpc(t, srv, "s1", "harness.verdict", map[string]any{"run": run.ID, "step": 1, "result": "pass"}), CodeNeedsOperator)
	if rows := ledgerRows(t, srv, domain.EventHarnessVerdict); len(rows) != 0 {
		t.Fatalf("a refused verdict was recorded: %v", rows)
	}
}

func TestHarnessFailAddsTodo(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{})
	run := harnessUp(t, srv, "", map[string]any{"session": "s1", "scenario": "plain", "wait": true})
	rpcOK(t, srv, "", "harness.verdict", map[string]any{"run": run.ID, "step": 1, "result": "pass"}, nil)
	var res todoListResult
	rpcOK(t, srv, "", "todo.get", map[string]any{"session": "s1"}, &res)
	if len(res.List.Items) != 0 {
		t.Fatalf("a pass added to-dos: %+v", res.List.Items)
	}
	var v struct {
		Type    string          `json:"type"`
		Verdict harness.Verdict `json:"verdict"`
		Todo    string          `json:"todo"`
	}
	rpcOK(t, srv, "", "harness.verdict", map[string]any{"run": run.ID, "step": 1, "result": "fail", "note": "UI says Refunded but orders.status stays paid\nsecond line"}, &v)
	if v.Type != "harness_verdict" || v.Verdict.Result != "fail" || v.Todo == "" {
		t.Fatalf("verdict = %+v", v)
	}
	rpcOK(t, srv, "", "todo.get", map[string]any{"session": "s1"}, &res)
	if len(res.List.Items) != 1 {
		t.Fatalf("items = %+v", res.List.Items)
	}
	it := res.List.Items[0]
	if it.Text != "Harness: plain step 1: UI says Refunded but orders.status stays paid" || it.Ref != "harness:plain:1" || it.State != domain.TodoPending {
		t.Fatalf("to-do = %+v", it)
	}
	// A second fail of the same step updates the item instead of adding one.
	rpcOK(t, srv, "", "harness.verdict", map[string]any{"run": run.ID, "step": 1, "result": "fail", "note": "still broken"}, nil)
	rpcOK(t, srv, "", "todo.get", map[string]any{"session": "s1"}, &res)
	if len(res.List.Items) != 1 || res.List.Items[0].Text != "Harness: plain step 1: still broken" {
		t.Fatalf("items after a second fail = %+v", res.List.Items)
	}
	deadline := time.Now().Add(2 * time.Second)
	for srv.Steerer().Status("s1").Queued == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if srv.Steerer().Status("s1").Queued == 0 {
		t.Fatal("no steering note queued for the failed step")
	}
	if rows := ledgerRows(t, srv, domain.EventHarnessVerdict); len(rows) != 3 || rows[1]["result"] != "fail" || rows[1]["step"] != float64(1) {
		t.Fatalf("harness.verdict = %v", rows)
	}
}

func TestHarnessDirty(t *testing.T) {
	srv, dir := harnessServer(t, harnessSetup{})
	if err := os.WriteFile(filepath.Join(dir, "wip.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := wantHarnessCode(t, "dirty up", rpc(t, srv, "", "harness.up", map[string]any{"session": "s1", "scenario": "plain"}), harness.CodeDirty)
	if paths, _ := data["paths"].([]any); !slices.Contains(paths, any("wip.go")) {
		t.Fatalf("data = %v", data)
	}
	// Any caller may ask for a dirty run.
	run := harnessUp(t, srv, "s1", map[string]any{"session": "s1", "scenario": "plain", "dirty": true, "wait": true})
	if !run.Dirty || len(run.DiffSHA256) != 64 || run.SHA == "" {
		t.Fatalf("run = %+v", run)
	}
	if rows := ledgerRows(t, srv, domain.EventHarnessStarted); len(rows) != 1 || rows[0]["dirty"] != true || rows[0]["diff_sha256"] != run.DiffSHA256 {
		t.Fatalf("harness.started = %v", rows)
	}
}

func TestHarnessDirtyVerdict(t *testing.T) {
	srv, dir := harnessServer(t, harnessSetup{})
	if err := os.WriteFile(filepath.Join(dir, "wip.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := harnessUp(t, srv, "", map[string]any{"session": "s1", "scenario": "plain", "dirty": true, "wait": true})
	var v struct {
		Verdict harness.Verdict `json:"verdict"`
	}
	rpcOK(t, srv, "", "harness.verdict", map[string]any{"run": run.ID, "step": 0, "result": "fail", "note": "broken"}, &v)
	if !v.Verdict.Dirty || v.Verdict.DiffSHA256 != run.DiffSHA256 || v.Verdict.SHA != run.SHA {
		t.Fatalf("verdict = %+v", v.Verdict)
	}
	rows := ledgerRows(t, srv, domain.EventHarnessVerdict)
	if len(rows) != 1 || rows[0]["dirty"] != true || rows[0]["diff_sha256"] != run.DiffSHA256 || rows[0]["sha"] != run.SHA {
		t.Fatalf("harness.verdict = %v", rows)
	}
	var res todoListResult
	rpcOK(t, srv, "", "todo.get", map[string]any{"session": "s1"}, &res)
	if len(res.List.Items) != 1 || res.List.Items[0].Ref != "harness:plain:all" {
		t.Fatalf("scenario fail to-do = %+v", res.List.Items)
	}
}

func TestHarnessConfigSource(t *testing.T) {
	override := map[string]any{
		"version":   1,
		"app":       map[string]any{"run": "serve"},
		"scenarios": []any{map[string]any{"id": "from-config"}},
	}
	srv, _ := harnessServer(t, harnessSetup{repos: map[string]config.RepoBootstrap{"O/R": {Harness: override}}})
	wantHarnessCode(t, "file scenario", rpc(t, srv, "", "harness.up", map[string]any{"session": "s1", "scenario": "plain"}), harness.CodeUnknownScenario)
	run := harnessUp(t, srv, "", map[string]any{"session": "s1", "scenario": "from-config"})
	if run.Source != harness.SourceConfig {
		t.Fatalf("source = %s, want config", run.Source)
	}
	if rows := ledgerRows(t, srv, domain.EventHarnessStarted); len(rows) != 1 || rows[0]["source"] != "config" {
		t.Fatalf("harness.started = %v", rows)
	}
}

func TestHarnessImageMissing(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{images: []string{"golang:1.25", harness.EdgeImage}})
	data := wantHarnessCode(t, "up", rpc(t, srv, "", "harness.up", map[string]any{"session": "s1", "scenario": "pg"}), harness.CodeImageMissing)
	if data["image"] != "postgres:16" {
		t.Fatalf("data = %v", data)
	}
}

func TestHarnessTooLarge(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{runMemory: 3 << 30})
	data := wantHarnessCode(t, "up", rpc(t, srv, "", "harness.up", map[string]any{"session": "s1", "scenario": "pg"}), harness.CodeTooLarge)
	if data["limit"] != float64(3<<30) || data["sum"].(float64) <= float64(3<<30) {
		t.Fatalf("data = %v", data)
	}
}

func TestHarnessForceNeedsOperator(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{})
	f := withFakePressure(srv)
	wantHarnessCode(t, "session force", rpc(t, srv, "s1", "harness.up", map[string]any{"session": "s1", "scenario": "plain", "force": true}), CodeNeedsOperator)
	since := closeGate(t, srv, f)
	wantPressureRefusal(t, "gated up", rpc(t, srv, "s1", "harness.up", map[string]any{"session": "s1", "scenario": "plain"}), since)
	run := harnessUp(t, srv, "", map[string]any{"session": "s1", "scenario": "plain", "force": true})
	if run.ID == "" {
		t.Fatal("operator force did not start a run")
	}
}

func TestHarnessDebugNeedsAppDebug(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{})
	resp := rpc(t, srv, "", "harness.up", map[string]any{"session": "s1", "scenario": "plain", "debug": true})
	wantHarnessCode(t, "debug up", resp, harness.CodeInvalid)
	if !strings.Contains(resp.Error.Message, "app.debug") {
		t.Fatalf("message = %q", resp.Error.Message)
	}
}

func TestHarnessCheckAndLogs(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{})
	run := harnessUp(t, srv, "s1", map[string]any{"session": "s1", "scenario": "plain", "wait": true})
	wantHarnessCode(t, "no checks", rpc(t, srv, "s1", "harness.check", map[string]any{"run": run.ID}), CodeInvalidParams)
	var logs struct {
		Type  string   `json:"type"`
		Lines []string `json:"lines"`
	}
	rpcOK(t, srv, "s1", "harness.logs", map[string]any{"run": run.ID, "service": "app"}, &logs)
	if logs.Type != "harness_logs" || logs.Lines == nil {
		t.Fatalf("logs = %+v", logs)
	}
	wantHarnessCode(t, "bad service", rpc(t, srv, "s1", "harness.logs", map[string]any{"run": run.ID, "service": "nope"}), CodeInvalidParams)
}

func TestHarnessDropSessionDownsRun(t *testing.T) {
	srv, _ := harnessServer(t, harnessSetup{})
	run := harnessUp(t, srv, "", map[string]any{"session": "s1", "scenario": "plain", "wait": true})
	sess, err := srv.repo.Get(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.dropSession(context.Background(), *sess, "forget"); err != nil {
		t.Fatal(err)
	}
	got, err := srv.harness.d.Manager.Get(run.ID)
	if err != nil || got.State != harness.StateStopped || got.StopReason != harness.StopSessionGone {
		t.Fatalf("run after drop: %+v %v", got, err)
	}
}
