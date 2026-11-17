package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/harness/edge"
)

// fakeDocker answers compose and image calls from memory.
type fakeDocker struct {
	mu     sync.Mutex
	images map[string]string
	calls  []string
	// answer may override a compose call: ok false falls through to success.
	answer func(args []string) (out string, exit int, ok bool)
	stdin  []string
}

func newFakeDocker(images ...string) *fakeDocker {
	d := &fakeDocker{images: map[string]string{}}
	for i, ref := range images {
		d.images[ref] = fmt.Sprintf("sha256:%064x", i+1)
	}
	return d
}

func (d *fakeDocker) ImageID(_ context.Context, ref string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if id, ok := d.images[ref]; ok {
		return id, nil
	}
	return "", ErrImageNotFound
}

func (d *fakeDocker) Compose(_ context.Context, call ComposeCall) (string, int, error) {
	d.mu.Lock()
	d.calls = append(d.calls, strings.Join(call.Args, " "))
	if call.Stdin != nil {
		b, _ := io.ReadAll(call.Stdin)
		d.stdin = append(d.stdin, string(b))
	}
	answer := d.answer
	d.mu.Unlock()
	if answer != nil {
		if out, exit, ok := answer(call.Args); ok {
			return out, exit, nil
		}
	}
	if len(call.Args) > 0 && call.Args[0] == "port" {
		return "127.0.0.1:41873\n", 0, nil
	}
	return "", 0, nil
}

func (d *fakeDocker) called(prefix string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, c := range d.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

type fakeEdge struct{}

func (fakeEdge) Health(context.Context, string, string) (edge.Health, error) {
	return edge.Health{Outer: "10.0.0.2"}, nil
}

type recorded struct {
	session string
	kind    domain.EventKind
	payload map[string]any
}

type recorder struct {
	mu   sync.Mutex
	rows []recorded
}

func (r *recorder) record(session string, _ domain.EventActor, kind domain.EventKind, payload map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, recorded{session, kind, payload})
}

func (r *recorder) kinds() []domain.EventKind {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.EventKind
	for _, row := range r.rows {
		out = append(out, row.kind)
	}
	return out
}

func (r *recorder) last(kind domain.EventKind) map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.rows) - 1; i >= 0; i-- {
		if r.rows[i].kind == kind {
			return r.rows[i].payload
		}
	}
	return nil
}

const testHarnessFile = `version: 1
app:
  build: echo built
  run: exec httpd -f -p 8080
  ports: [{name: web, container: 8080}]
services:
  db:
    image: postgres:16
    env: {POSTGRES_PASSWORD: test}
    ready: pg_isready
    database: {kind: postgres, name: app}
    limits: {memory: 1g}
scenarios:
  - id: plain
    steps:
      - do: "Open {{web}}"
        expect: ok
      - do: "Press refund"
    checks:
      - {name: ok, run: "true"}
      - {name: bad, run: "exit 3"}
  - id: pg
    services: [db]
    fixtures: [{sql: testdata/seed.sql, into: db}]
    setup: [{run: echo setup}]
`

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// testRepo is a committed checkout holding the harness file.
func testRepo(t *testing.T, file string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(dir, ".aiman"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "testdata"), 0o750); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "init", "-q", "-b", "main")
	gitRun(t, dir, "config", "user.name", "T")
	gitRun(t, dir, "config", "user.email", "t@example.com")
	writeTestFile(t, filepath.Join(dir, FilePath), file)
	writeTestFile(t, filepath.Join(dir, "testdata", "seed.sql"), "insert into t values (1);\n")
	writeTestFile(t, filepath.Join(dir, "go.mod"), "module x\n\ngo 1.25\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

type testManager struct {
	*Manager
	docker *fakeDocker
	rec    *recorder
	dir    string
	now    *time.Time
}

func newTestManager(t *testing.T, mut ...func(*Options)) *testManager {
	t.Helper()
	d := newFakeDocker("golang:1.25", "postgres:16", EdgeImage)
	rec := &recorder{}
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	tm := &testManager{docker: d, rec: rec, dir: filepath.Join(t.TempDir(), "harness"), now: &now}
	opts := Options{
		Dir:      tm.dir,
		AimanBin: "/usr/local/bin/aiman",
		UID:      1000, GID: 1000,
		Docker:   d,
		Edge:     fakeEdge{},
		Record:   rec.record,
		AppImage: func(string, string) (string, error) { return "golang:1.25", nil },
		Now:      func() time.Time { return *tm.now },
	}
	for _, m := range mut {
		m(&opts)
	}
	m, err := NewManager(opts)
	if err != nil {
		t.Fatal(err)
	}
	tm.Manager = m
	t.Cleanup(m.Close)
	return tm
}

func (tm *testManager) upReady(t *testing.T, repo, scenario string) Run {
	t.Helper()
	run, err := tm.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: scenario, Actor: domain.EventActorUser})
	if err != nil {
		t.Fatal(err)
	}
	got := tm.Wait(context.Background(), run.ID, 5*time.Second)
	if got.State != StateReady {
		t.Fatalf("run state %s (%s: %s), want ready", got.State, got.FailedStep, got.Error)
	}
	return got
}

func TestManagerUpReady(t *testing.T) {
	tm := newTestManager(t)
	repo := testRepo(t, testHarnessFile)
	run := tm.upReady(t, repo, "plain")

	if run.Source != SourceFile || run.Dirty || run.SHA == "" || run.Network != "offline" {
		t.Fatalf("run = %+v", run)
	}
	if len(run.Ports) != 1 || run.Ports[0].Name != "web" || run.Ports[0].HostPort != 41873 {
		t.Fatalf("ports = %+v", run.Ports)
	}
	if len(run.Steps) != 2 || run.Steps[0].N != 1 || run.Steps[0].Do != "Open {{web}}" {
		t.Fatalf("steps = %+v", run.Steps)
	}
	if run.Images["app"] == "" || run.Images["edge"] == "" {
		t.Fatalf("images = %v", run.Images)
	}
	for _, want := range []string{"up --no-start", "run --rm app sh -c echo built", "up -d app", "up -d --wait edge", "port edge 10001"} {
		if tm.docker.called(want) != 1 {
			t.Errorf("compose %q called %d times; calls %q", want, tm.docker.called(want), tm.docker.calls)
		}
	}
	if !slices.Equal(tm.rec.kinds(), []domain.EventKind{domain.EventHarnessStarted, domain.EventHarnessReady}) {
		t.Fatalf("ledger = %v", tm.rec.kinds())
	}
	// The exported tree and the compose file are in the run directory.
	runDir := filepath.Join(tm.dir, "s1", run.ID)
	for _, p := range []string{"src/.aiman/harness.yaml", "src/go.mod", "sandbox/compose.yaml", "control/token", "run.json", "result.json"} {
		if _, err := os.Stat(filepath.Join(runDir, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	if fi, err := os.Stat(filepath.Join(runDir, "control", "token")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token mode: %v %v", fi, err)
	}
}

func TestManagerServicesFixturesSetup(t *testing.T) {
	tm := newTestManager(t)
	repo := testRepo(t, testHarnessFile)
	tm.upReady(t, repo, "pg")
	for _, want := range []string{"up -d --wait --wait-timeout 60 db", "exec -T db psql -v ON_ERROR_STOP=1 -U postgres -d app", "run --rm app sh -c echo setup"} {
		if tm.docker.called(want) != 1 {
			t.Errorf("compose %q called %d times; calls %q", want, tm.docker.called(want), tm.docker.calls)
		}
	}
	if len(tm.docker.stdin) != 1 || !strings.Contains(tm.docker.stdin[0], "insert into t") {
		t.Fatalf("fixture stdin = %q", tm.docker.stdin)
	}
	// The postgres init script sits beside compose.yaml, read-only.
	runs, _ := tm.Runs("s1")
	fi, err := os.Stat(filepath.Join(tm.dir, "s1", runs[0].ID, "sandbox", InitDBFile))
	if err != nil || fi.Mode().Perm() != 0o444 {
		t.Fatalf("initdb: %v %v", fi, err)
	}
}

func TestManagerServiceFailed(t *testing.T) {
	tm := newTestManager(t)
	tm.docker.answer = func(args []string) (string, int, bool) {
		if slices.Contains(args, "db") && args[0] == "up" {
			return "db is unhealthy\nFATAL: boom", 1, true
		}
		return "", 0, false
	}
	repo := testRepo(t, testHarnessFile)
	run, err := tm.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: "pg"})
	if err != nil {
		t.Fatal(err)
	}
	got := tm.Wait(context.Background(), run.ID, 5*time.Second)
	if got.State != StateFailed || got.FailedStep != "service db" || got.FailedService != "db" || !strings.Contains(got.Error, "FATAL: boom") {
		t.Fatalf("run = %+v", got)
	}
	if tm.docker.called("down -v") != 1 {
		t.Fatalf("a failed run must be taken down; calls %q", tm.docker.calls)
	}
	p := tm.rec.last(domain.EventHarnessFailed)
	if p == nil || p["step"] != "service db" || p["exit"] != 1 {
		t.Fatalf("harness.failed = %v", p)
	}
	// The session may start again.
	if _, err := tm.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: "plain"}); err != nil {
		t.Fatalf("up after a failed run: %v", err)
	}
}

func TestManagerDirty(t *testing.T) {
	tm := newTestManager(t)
	repo := testRepo(t, testHarnessFile)
	writeTestFile(t, filepath.Join(repo, "new.txt"), "x")
	_, err := tm.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: "plain"})
	var he *Error
	if !errors.As(err, &he) || he.Code != CodeDirty || !slices.Contains(he.Paths, "new.txt") {
		t.Fatalf("err = %v (%+v), want harness_dirty naming new.txt", err, he)
	}
	run, err := tm.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: "plain", Dirty: true})
	if err != nil {
		t.Fatal(err)
	}
	if !run.Dirty || len(run.DiffSHA256) != 64 {
		t.Fatalf("run = %+v", run)
	}
	// A dirty run exports the working tree: untracked files, not ignored ones.
	if _, err := os.Stat(filepath.Join(tm.dir, "s1", run.ID, "src", "new.txt")); err != nil {
		t.Fatalf("untracked file not exported: %v", err)
	}
	p := tm.rec.last(domain.EventHarnessStarted)
	if p["dirty"] != true || p["diff_sha256"] != run.DiffSHA256 || p["source"] != "file" {
		t.Fatalf("harness.started = %v", p)
	}
}

func TestManagerReviewSkipsCleanCheck(t *testing.T) {
	tm := newTestManager(t)
	repo := testRepo(t, testHarnessFile)
	writeTestFile(t, filepath.Join(repo, "staged.txt"), "x")
	gitRun(t, repo, "add", "staged.txt")
	run, err := tm.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, SHA: "HEAD", Scenario: "plain", SkipCleanCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	if run.Dirty {
		t.Fatalf("a review run is not dirty: %+v", run)
	}
	if _, err := os.Stat(filepath.Join(tm.dir, "s1", run.ID, "src", "staged.txt")); err == nil {
		t.Fatal("a review run exports the commit, not the index")
	}
}

func TestManagerImageMissing(t *testing.T) {
	cases := map[string]struct {
		file   string
		images []string
		want   string
	}{
		"service not on host": {testHarnessFile, []string{"golang:1.25", EdgeImage}, "postgres:16"},
		"service not allowed": {strings.Replace(testHarnessFile, "postgres:16", "redis:7", 1), []string{"golang:1.25", "redis:7", EdgeImage}, "redis:7"},
		"edge not on host":    {testHarnessFile, []string{"golang:1.25", "postgres:16"}, EdgeImage},
		"app not on host":     {testHarnessFile, []string{"postgres:16", EdgeImage}, "golang:1.25"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tm := newTestManager(t)
			tm.docker.images = newFakeDocker(tc.images...).images
			repo := testRepo(t, tc.file)
			_, err := tm.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: "pg"})
			var he *Error
			if !errors.As(err, &he) || he.Code != CodeImageMissing || he.Image != tc.want {
				t.Fatalf("err = %v (%+v), want harness_image_missing %s", err, he, tc.want)
			}
			if runs, _ := tm.Runs("s1"); len(runs) != 0 {
				t.Fatalf("a refused run left %d run records", len(runs))
			}
		})
	}
}

func TestManagerTooLarge(t *testing.T) {
	tm := newTestManager(t, func(o *Options) { o.RunMemory = 3 << 30 })
	repo := testRepo(t, testHarnessFile)
	_, err := tm.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: "pg"})
	var he *Error
	if !errors.As(err, &he) || he.Code != CodeTooLarge || he.Limit != 3<<30 {
		t.Fatalf("err = %v, want harness_too_large", err)
	}
}

func TestManagerRunningAndBusy(t *testing.T) {
	tm := newTestManager(t, func(o *Options) { o.MaxRuns = 1 })
	repo := testRepo(t, testHarnessFile)
	tm.upReady(t, repo, "plain")
	_, err := tm.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: "plain"})
	if !errors.Is(err, &Error{Code: CodeRunning}) {
		t.Fatalf("second run of s1: %v", err)
	}
	_, err = tm.Up(context.Background(), UpRequest{Session: "s2", Repo: repo, Scenario: "plain"})
	if !errors.Is(err, &Error{Code: CodeBusy}) {
		t.Fatalf("run over max_runs: %v", err)
	}
}

func TestManagerSubnetRetry(t *testing.T) {
	tm := newTestManager(t)
	first := true
	tm.docker.answer = func(args []string) (string, int, bool) {
		if strings.Join(args, " ") == "up --no-start" && first {
			first = false
			return "Error response from daemon: Pool overlaps with other one on this address space", 1, true
		}
		return "", 0, false
	}
	repo := testRepo(t, testHarnessFile)
	run := tm.upReady(t, repo, "plain")
	if tm.docker.called("up --no-start") != 2 || run.InnerSubnet == "10.231.0.0/25" {
		t.Fatalf("subnet retry: calls %q, inner %s", tm.docker.calls, run.InnerSubnet)
	}
}

func TestManagerCheck(t *testing.T) {
	tm := newTestManager(t)
	tm.docker.answer = func(args []string) (string, int, bool) {
		if strings.Join(args, " ") == "run --rm --no-deps app sh -c exit 3" {
			return "failing\n", 3, true
		}
		return "", 0, false
	}
	repo := testRepo(t, testHarnessFile)
	run := tm.upReady(t, repo, "plain")
	results, err := tm.Check(context.Background(), run.ID, "", domain.EventActorAgent)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Name != "ok" || *results[0].Exit != 0 || results[1].Name != "bad" || *results[1].Exit != 3 || !strings.Contains(results[1].Output, "failing") {
		t.Fatalf("results = %+v", results)
	}
	if _, err := tm.Check(context.Background(), run.ID, "nope", domain.EventActorAgent); err == nil {
		t.Fatal("unknown check accepted")
	}
	got, _ := tm.Get(run.ID)
	if got.Checks[1].Exit == nil || *got.Checks[1].Exit != 3 {
		t.Fatalf("stored checks = %+v", got.Checks)
	}
	if p := tm.rec.last(domain.EventHarnessCheck); p["name"] != "bad" || p["exit"] != 3 {
		t.Fatalf("harness.check = %v", p)
	}
}

func TestManagerVerdict(t *testing.T) {
	tm := newTestManager(t)
	repo := testRepo(t, testHarnessFile)
	run := tm.upReady(t, repo, "plain")
	v, got, err := tm.Verdict(run.ID, 2, "fail", "UI says Refunded\nbut not", "operator")
	if err != nil {
		t.Fatal(err)
	}
	if v.Step != 2 || v.Result != "fail" || v.SHA != run.SHA || got.Steps[1].Result != "fail" {
		t.Fatalf("verdict %+v, run steps %+v", v, got.Steps)
	}
	if p := tm.rec.last(domain.EventHarnessVerdict); p["step"] != 2 || p["result"] != "fail" || p["scenario"] != "plain" || p["dirty"] != false {
		t.Fatalf("harness.verdict = %v", p)
	}
	for _, bad := range []struct {
		step   int
		result string
	}{{3, "pass"}, {-1, "pass"}, {1, "maybe"}} {
		if _, _, err := tm.Verdict(run.ID, bad.step, bad.result, "", "operator"); err == nil {
			t.Errorf("verdict step %d %s accepted", bad.step, bad.result)
		}
	}
	// Step 0 is the whole scenario, and a stopped run still takes verdicts.
	if err := tm.Down(context.Background(), run.ID, StopDown); err != nil {
		t.Fatal(err)
	}
	if _, got, err = tm.Verdict(run.ID, 0, "pass", "", "operator"); err != nil || got.Result != "pass" {
		t.Fatalf("scenario verdict after down: %v %+v", err, got)
	}
}

func TestManagerDownPrunesAndForgetsLive(t *testing.T) {
	tm := newTestManager(t)
	repo := testRepo(t, testHarnessFile)
	run := tm.upReady(t, repo, "plain")
	if err := tm.Down(context.Background(), run.ID, StopDown); err != nil {
		t.Fatal(err)
	}
	got, _ := tm.Get(run.ID)
	if got.State != StateStopped || got.StopReason != StopDown {
		t.Fatalf("run = %+v", got)
	}
	if _, err := os.Stat(filepath.Join(tm.dir, "s1", run.ID, "src")); !os.IsNotExist(err) {
		t.Fatalf("src kept after down: %v", err)
	}
	if p := tm.rec.last(domain.EventHarnessStopped); p["reason"] != "down" {
		t.Fatalf("harness.stopped = %v", p)
	}
	if err := tm.Down(context.Background(), run.ID, StopDown); !errors.Is(err, &Error{Code: CodeNotRunning}) {
		t.Fatalf("second down: %v", err)
	}
	// Only the newest five run directories stay.
	for range 6 {
		r := tm.upReady(t, repo, "plain")
		if err := tm.Down(context.Background(), r.ID, StopDown); err != nil {
			t.Fatal(err)
		}
	}
	if runs, _ := tm.Runs("s1"); len(runs) != KeepRuns {
		t.Fatalf("kept %d runs, want %d", len(runs), KeepRuns)
	}
}

func TestManagerDropAndIdle(t *testing.T) {
	tm := newTestManager(t, func(o *Options) { o.IdleTimeout = time.Hour })
	repo := testRepo(t, testHarnessFile)
	a := tm.upReady(t, repo, "plain")
	tm.Drop(context.Background(), "s1")
	if got, _ := tm.Get(a.ID); got.State != StateStopped || got.StopReason != StopSessionGone {
		t.Fatalf("after drop: %+v", got)
	}
	b := tm.upReady(t, repo, "plain")
	*tm.now = tm.now.Add(59 * time.Minute)
	tm.ReapIdle(context.Background())
	if got, _ := tm.Get(b.ID); got.State != StateReady {
		t.Fatalf("reaped before the idle timeout: %+v", got)
	}
	*tm.now = tm.now.Add(2 * time.Minute)
	tm.ReapIdle(context.Background())
	if got, _ := tm.Get(b.ID); got.State != StateStopped || got.StopReason != StopIdle {
		t.Fatalf("after idle: %+v", got)
	}
}

func TestManagerCleanStale(t *testing.T) {
	tm := newTestManager(t)
	repo := testRepo(t, testHarnessFile)
	run := tm.upReady(t, repo, "plain")
	// A new serve finds the run on disk but not in memory.
	fresh, err := NewManager(tm.opts)
	if err != nil {
		t.Fatal(err)
	}
	fresh.CleanStale(context.Background())
	got, _ := fresh.Get(run.ID)
	if got.State != StateStopped || got.StopReason != StopServeRestart {
		t.Fatalf("stale run = %+v", got)
	}
	if tm.docker.called("down -v") != 1 {
		t.Fatalf("stale project not taken down; calls %q", tm.docker.calls)
	}
}
