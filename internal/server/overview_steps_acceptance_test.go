package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/overview"
)

// Acceptance tests for overview-status (design rev 2, piece P1). They read
// the wire shape, so the Go names inside the run stay the piece's choice.

type wireStep struct {
	Name      string     `json:"name"`
	Label     string     `json:"label"`
	State     string     `json:"state"`
	StartedAt *time.Time `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
	Detail    string     `json:"detail"`
	Error     string     `json:"error"`
}

type wireRun struct {
	ID       string     `json:"id"`
	Session  string     `json:"session"`
	State    string     `json:"state"`
	Error    string     `json:"error"`
	Sessions int        `json:"sessions"`
	NeedsYou *int       `json:"needs_you"`
	Markdown string     `json:"markdown"`
	Report   any        `json:"report"`
	Steps    []wireStep `json:"steps"`
}

var wantStepNames = []string{"collect", "bundle", "start_agent", "report", "cleanup"}

const twoNeedsOverview = "## In progress\n- parent: adding the parser\n\n## Needs you\n- approve the design\n- answer the question\n\n## Next\n1. parent: open the PR\n\n## Risks\n- none\n"

func decodeWire(t *testing.T, v any) wireRun {
	t.Helper()
	b, _ := json.Marshal(v)
	var r wireRun
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return r
}

func startOverviewWire(t *testing.T, srv *Server) wireRun {
	t.Helper()
	resp := rpc(t, srv, "", "overview.run", map[string]any{})
	if resp.Error != nil {
		t.Fatalf("overview.run: %+v", resp.Error)
	}
	return decodeWire(t, resp.Result.(map[string]any)["run"])
}

// getWire answers overview.get with params; latest is nil when absent.
func getWire(t *testing.T, srv *Server, params map[string]any) (map[string]any, *wireRun) {
	t.Helper()
	resp := rpc(t, srv, "", "overview.get", params)
	if resp.Error != nil {
		t.Fatalf("overview.get: %+v", resp.Error)
	}
	res := resp.Result.(map[string]any)
	if l, ok := res["latest"]; ok && l != nil {
		r := decodeWire(t, l)
		return res, &r
	}
	return res, nil
}

// waitRunWire polls overview.get {run} until ok holds, for at most 5s.
func waitRunWire(t *testing.T, srv *Server, id string, ok func(wireRun) bool) wireRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last wireRun
	for time.Now().Before(deadline) {
		res, _ := getWire(t, srv, map[string]any{"run": id})
		last = decodeWire(t, res["run"])
		if ok(last) {
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run never reached the wanted state; last %+v", last)
	return last
}

func stepOf(t *testing.T, r wireRun, name string) wireStep {
	t.Helper()
	for _, s := range r.Steps {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no step %q in %+v", name, r.Steps)
	return wireStep{}
}

func TestOverviewStepsRunAnswersAtOnceWithPendingSteps(t *testing.T) {
	srv, creator, _, _ := overviewServer(t)
	release := make(chan struct{})
	creator.onCreate = func() { <-release }
	defer close(release)

	start := time.Now()
	run := startOverviewWire(t, srv)
	if time.Since(start) > 2*time.Second {
		t.Fatalf("overview.run waited %v for the agent", time.Since(start))
	}
	if run.State != OverviewStarting || run.ID == "" {
		t.Fatalf("run %+v, want starting", run)
	}
	if len(run.Steps) != len(wantStepNames) {
		t.Fatalf("steps %+v, want %v", run.Steps, wantStepNames)
	}
	for i, s := range run.Steps {
		if s.Name != wantStepNames[i] || s.Label == "" || strings.Contains(s.Name, " ") {
			t.Fatalf("step %d = %+v, want name %q with a label", i, s, wantStepNames[i])
		}
	}
	// The run is on disk before the background work, and a second run is refused.
	if !pathExists(filepath.Join(srv.overviewBase(), run.ID+".json")) {
		t.Fatal("run not saved before the answer")
	}
	wantCode(t, rpc(t, srv, "", "overview.run", map[string]any{}), CodeOverviewRunning)

	got := waitRunWire(t, srv, run.ID, func(r wireRun) bool { return stepOf(t, r, "start_agent").State == "running" })
	if c := stepOf(t, got, "collect"); c.State != "done" || !strings.Contains(c.Detail, "1 session") || c.StartedAt == nil || c.EndedAt == nil {
		t.Fatalf("collect %+v", c)
	}
	if b := stepOf(t, got, "bundle"); b.State != "done" {
		t.Fatalf("bundle %+v", b)
	}
	if srv.OverviewStarting() != 1 {
		t.Fatalf("OverviewStarting = %d while the agent starts", srv.OverviewStarting())
	}
}

func TestOverviewStepsReachDoneAndLatestCarriesNeedsYou(t *testing.T) {
	srv, _, _, base := overviewServer(t)
	srv.overviewPoll = 10 * time.Millisecond
	ch, cancel := srv.hub.Subscribe()
	defer cancel()

	run := startOverviewWire(t, srv)
	got := waitRunWire(t, srv, run.ID, func(r wireRun) bool { return r.State == OverviewRunning })
	if got.Session == "" || stepOf(t, got, "start_agent").State != "done" || stepOf(t, got, "report").State != "running" {
		t.Fatalf("running run %+v", got)
	}
	if srv.OverviewStarting() != 0 {
		t.Fatalf("OverviewStarting = %d after the agent started", srv.OverviewStarting())
	}
	if err := os.WriteFile(filepath.Join(base, run.ID, overview.ReportFile), []byte(twoNeedsOverview), 0o600); err != nil {
		t.Fatal(err)
	}
	done := waitRunWire(t, srv, run.ID, func(r wireRun) bool { return r.State == OverviewDone })
	for _, name := range wantStepNames {
		if s := stepOf(t, done, name); s.State != "done" {
			t.Fatalf("step %s = %+v in a done run", name, s)
		}
	}

	res, latest := getWire(t, srv, map[string]any{})
	if latest == nil || latest.ID != run.ID || latest.State != OverviewDone || latest.NeedsYou == nil || *latest.NeedsYou != 2 {
		t.Fatalf("latest %+v", latest)
	}
	if latest.Markdown != "" || latest.Report != nil || len(latest.Steps) != len(wantStepNames) {
		t.Fatalf("latest carries text or lacks steps: %+v", latest)
	}
	if decodeWire(t, res["run"]).Markdown != twoNeedsOverview {
		t.Fatal("run lost its markdown")
	}
	sum, latest2 := getWire(t, srv, map[string]any{"summary": true})
	if _, has := sum["run"]; has || latest2 == nil || latest2.ID != run.ID {
		t.Fatalf("summary answer %+v", sum)
	}

	sawStep := false
	for {
		select {
		case ev := <-ch:
			if ev.Type == "overview" && ev.ID == run.ID && ev.Step != "" {
				sawStep = true
			}
			continue
		default:
		}
		break
	}
	if !sawStep {
		t.Fatal("no overview hub event named a step")
	}
}

func TestOverviewStepsCreateFailureFailsStartAgent(t *testing.T) {
	srv, creator, _, _ := overviewServer(t)
	creator.failWith = func(domain.SessionConfig) error { return errors.New("boot wait timed out") }

	run := startOverviewWire(t, srv)
	got := waitRunWire(t, srv, run.ID, func(r wireRun) bool { return r.State == OverviewFailed })
	sa := stepOf(t, got, "start_agent")
	if sa.State != "failed" || !strings.Contains(sa.Error, "boot wait timed out") {
		t.Fatalf("start_agent %+v", sa)
	}
	if !strings.HasPrefix(got.Error, sa.Label+": ") || !strings.Contains(got.Error, "boot wait timed out") {
		t.Fatalf("run error %q, want %q prefix", got.Error, sa.Label+": ")
	}
	if r := stepOf(t, got, "report"); r.State != "pending" {
		t.Fatalf("report %+v after a failed start", r)
	}
	if srv.OverviewStarting() != 0 {
		t.Fatalf("OverviewStarting = %d after a failed start", srv.OverviewStarting())
	}
	// The one-run lock is free again, and latest shows the failure.
	_, latest := getWire(t, srv, map[string]any{})
	if latest == nil || latest.ID != run.ID || latest.State != OverviewFailed {
		t.Fatalf("latest %+v", latest)
	}
	creator.failWith = nil
	_ = startOverviewWire(t, srv)
}

func TestOverviewStepsLatestShowsANewerFailure(t *testing.T) {
	srv, creator, _, base := overviewServer(t)
	srv.overviewPoll = 10 * time.Millisecond
	first := startOverviewWire(t, srv)
	waitRunWire(t, srv, first.ID, func(r wireRun) bool { return r.State == OverviewRunning })
	if err := os.WriteFile(filepath.Join(base, first.ID, overview.ReportFile), []byte(goodOverview), 0o600); err != nil {
		t.Fatal(err)
	}
	waitRunWire(t, srv, first.ID, func(r wireRun) bool { return r.State == OverviewDone })

	creator.failWith = func(domain.SessionConfig) error { return errors.New("no terminal") }
	second := startOverviewWire(t, srv)
	waitRunWire(t, srv, second.ID, func(r wireRun) bool { return r.State == OverviewFailed })

	res, latest := getWire(t, srv, map[string]any{})
	if decodeWire(t, res["run"]).ID != first.ID {
		t.Fatalf("run is not the newest done run: %v", res["run"])
	}
	if latest == nil || latest.ID != second.ID || latest.State != OverviewFailed {
		t.Fatalf("latest %+v, want the newer failure", latest)
	}
}

func TestOverviewStepsCloseFailsTheRunningStep(t *testing.T) {
	srv, creator, _, _ := overviewServer(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	creator.onCreate = func() {
		close(entered)
		<-release
	}
	run := startOverviewWire(t, srv)
	<-entered
	closed := make(chan struct{})
	go func() {
		srv.CloseOverview()
		close(closed)
	}()
	// CloseOverview waits for the run; the create returns only when released.
	select {
	case <-closed:
		t.Fatal("CloseOverview returned while the run was still starting")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("CloseOverview did not return")
	}
	res, _ := getWire(t, srv, map[string]any{"run": run.ID})
	got := decodeWire(t, res["run"])
	if got.State != OverviewFailed {
		t.Fatalf("run %+v after close, want failed", got)
	}
	failed := 0
	for _, s := range got.Steps {
		if s.State == "failed" {
			failed++
		}
		if s.State == "running" {
			t.Fatalf("step %+v still running after close", s)
		}
	}
	if failed != 1 {
		t.Fatalf("steps %+v, want one failed", got.Steps)
	}
}

func TestOverviewStepsCleanStaleFailsTheRunningStep(t *testing.T) {
	srv, _, _, _ := overviewServer(t)
	run := startOverviewWire(t, srv)
	waitRunWire(t, srv, run.ID, func(r wireRun) bool { return r.State == OverviewRunning })
	// A new serve: nothing in memory, the run on disk with report running.
	srv.overviewMu.Lock()
	srv.overview.running = nil
	srv.overviewMu.Unlock()
	srv.CleanStaleOverviews(context.Background())
	res, _ := getWire(t, srv, map[string]any{"run": run.ID})
	got := decodeWire(t, res["run"])
	if got.State != OverviewFailed || !strings.Contains(got.Error, "serve restarted") {
		t.Fatalf("run %+v", got)
	}
	if r := stepOf(t, got, "report"); r.State != "failed" || !strings.Contains(r.Error, "serve restarted") {
		t.Fatalf("report %+v", r)
	}
}

func TestOverviewStepsCleanStaleFindsAnUnrecordedSession(t *testing.T) {
	srv, _, _, base := overviewServer(t)
	// A previous serve saved the hidden session but not its id on the run.
	sess := domain.Session{ID: "orphan-overview", Name: "overview-abc12345", Role: domain.SessionRoleOverview}
	if err := srv.repo.Save(context.Background(), &sess); err != nil {
		t.Fatal(err)
	}
	stale := map[string]any{
		"id": "abc12345", "agent": "codex", "state": OverviewStarting, "started_at": time.Now().UTC(),
		"steps": []map[string]any{
			{"name": "collect", "label": "collect sessions", "state": "done"},
			{"name": "bundle", "label": "write bundle", "state": "done"},
			{"name": "start_agent", "label": "start agent", "state": "running"},
			{"name": "report", "label": "write report", "state": "pending"},
			{"name": "cleanup", "label": "clean up", "state": "pending"},
		},
	}
	b, _ := json.Marshal(stale)
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "abc12345.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	srv.CleanStaleOverviews(context.Background())
	if _, ok := srv.sessionByID(context.Background(), "orphan-overview"); ok {
		t.Fatal("the unrecorded hidden session was not retired")
	}
	res, _ := getWire(t, srv, map[string]any{"run": "abc12345"})
	got := decodeWire(t, res["run"])
	if got.State != OverviewFailed || stepOf(t, got, "start_agent").State != "failed" {
		t.Fatalf("run %+v", got)
	}
}
