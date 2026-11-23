package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/overview"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/ptyruntime/ptyfake"
)

const goodOverview = "## In progress\n- parent: adding the parser\n\n## Needs you\n- none\n\n## Next\n1. parent: open the PR\n\n## Risks\n- none\n"

// overviewServer is the consult rig with a running fake terminal for the run,
// and a watcher that never ticks by itself.
func overviewServer(t *testing.T) (*Server, *fakeCreator, *ptyfake.FakePTY, string) {
	t.Helper()
	srv, creator, home := consultServer(t, []config.PreferredAgent{{Agent: "codex", Model: "gpt-5.4"}})
	fake := ptyfake.New()
	fake.Add(ptyruntime.SessionInfo{ID: "new-id", Status: ptyruntime.StatusRunning, Role: string(domain.SessionRoleOverview)})
	srv.pty = fake
	srv.overviewPoll = time.Hour
	t.Cleanup(srv.CloseOverview)
	return srv, creator, fake, filepath.Join(home, ".aiman", "overviews")
}

// startOverviewRun starts a run and waits until its agent runs.
func startOverviewRun(t *testing.T, srv *Server) OverviewRun {
	t.Helper()
	resp := rpc(t, srv, "", "overview.run", map[string]any{})
	if resp.Error != nil {
		t.Fatalf("overview.run: %+v", resp.Error)
	}
	b, _ := json.Marshal(resp.Result.(map[string]any)["run"])
	var run OverviewRun
	if err := json.Unmarshal(b, &run); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp := rpc(t, srv, "", "overview.get", map[string]any{"run": run.ID})
		if resp.Error != nil {
			t.Fatalf("overview.get: %+v", resp.Error)
		}
		b, _ := json.Marshal(resp.Result.(map[string]any)["run"])
		var got OverviewRun
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		if got.State != OverviewStarting {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("run %s never left starting", run.ID)
	return run
}

func getOverview(t *testing.T, srv *Server) OverviewRun {
	t.Helper()
	resp := rpc(t, srv, "", "overview.get", map[string]any{})
	if resp.Error != nil {
		t.Fatalf("overview.get: %+v", resp.Error)
	}
	b, _ := json.Marshal(resp.Result.(map[string]any)["run"])
	var run OverviewRun
	if err := json.Unmarshal(b, &run); err != nil {
		t.Fatal(err)
	}
	return run
}

func overviewWatchOf(srv *Server, base string, run OverviewRun) *overviewWatch {
	srv.overviewMu.Lock()
	live := srv.overview.running
	srv.overviewMu.Unlock()
	sess, _ := srv.sessionByID(context.Background(), run.Session)
	return &overviewWatch{base: base, run: live, sess: sess, timeout: config.DefaultOverviewTimeout}
}

func TestOverviewRunWritesTheBundleAndStartsAHiddenSession(t *testing.T) {
	srv, creator, _, base := overviewServer(t)
	srv.appendEvent(context.Background(), "p", domain.EventActorAiman, domain.EventChildLanded, map[string]any{"secret": "payload-text"})
	run := startOverviewRun(t, srv)
	if run.State != OverviewRunning || run.Agent != "codex" || run.Model != "gpt-5.4" || run.Sessions != 1 {
		t.Fatalf("run %+v", run)
	}
	cfg := creator.cfg
	if cfg.Role != domain.SessionRoleOverview || !cfg.Role.Hidden() || !cfg.AdHoc || !cfg.PromptFree {
		t.Fatalf("cfg %+v", cfg)
	}
	dir := filepath.Join(base, run.ID)
	if cfg.PreparedWorktree != dir || !strings.Contains(cfg.InitialPrompt, filepath.Join(dir, overview.ReportFile)) {
		t.Fatalf("worktree %q prompt %q", cfg.PreparedWorktree, cfg.InitialPrompt)
	}
	bundle, err := os.ReadFile(filepath.Join(dir, overview.BundleFile))
	if err != nil || !strings.Contains(string(bundle), "## Session parent (p)") || !strings.Contains(string(bundle), "t3 Add the parser") {
		t.Fatalf("bundle %q, %v", bundle, err)
	}
	if !strings.Contains(string(bundle), "aiman child.landed") || strings.Contains(string(bundle), "payload-text") {
		t.Fatalf("bundle events wrong:\n%s", bundle)
	}
	if strings.Contains(string(bundle), "overview-"+run.ID) {
		t.Fatal("the run's own hidden session is in the bundle")
	}
	wantCode(t, rpc(t, srv, "", "overview.run", map[string]any{}), CodeOverviewRunning)
}

func TestOverviewFinishesOnAValidReport(t *testing.T) {
	srv, _, fake, base := overviewServer(t)
	run := startOverviewRun(t, srv)
	w := overviewWatchOf(srv, base, run)
	if srv.overviewStep(context.Background(), w, time.Now()) {
		t.Fatal("ended with no report while the agent runs")
	}
	if err := os.WriteFile(filepath.Join(base, run.ID, overview.ReportFile), []byte(goodOverview), 0o600); err != nil {
		t.Fatal(err)
	}
	if !srv.overviewStep(context.Background(), w, time.Now()) {
		t.Fatal("a valid report did not end the run")
	}
	got := getOverview(t, srv)
	if got.State != OverviewDone || got.Report == nil || got.Report.Next[0] != "parent: open the PR" || got.Markdown != goodOverview {
		t.Fatalf("run %+v", got)
	}
	if pathExists(filepath.Join(base, run.ID)) {
		t.Fatal("scratch directory left")
	}
	if _, ok := srv.sessionByID(context.Background(), run.Session); ok {
		t.Fatal("the hidden session's row is still there")
	}
	found := false
	for _, k := range fake.Kills() {
		found = found || k == run.Session
	}
	if !found {
		t.Fatal("the agent was not stopped")
	}
	// A new run may start once the last one ended.
	_ = startOverviewRun(t, srv)
}

func TestOverviewRemindsOnceThenFailsWhenTheAgentEnds(t *testing.T) {
	srv, _, fake, base := overviewServer(t)
	run := startOverviewRun(t, srv)
	w := overviewWatchOf(srv, base, run)
	if err := os.WriteFile(filepath.Join(base, run.ID, overview.ReportFile), []byte("## In progress\n- x: y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv.overviewStep(context.Background(), w, time.Now())
	srv.overviewStep(context.Background(), w, time.Now())
	if !w.asked {
		t.Fatal("no reminder for an invalid report")
	}
	fake.Add(ptyruntime.SessionInfo{ID: run.Session, Status: ptyruntime.StatusExited, Role: string(domain.SessionRoleOverview)})
	if !srv.overviewStep(context.Background(), w, time.Now()) {
		t.Fatal("an ended agent did not end the run")
	}
	got := getOverview(t, srv)
	if got.State != OverviewFailed || !strings.Contains(got.Error, "missing ## Needs you") || got.Markdown != "" {
		t.Fatalf("run %+v", got)
	}
}

func TestOverviewTimesOut(t *testing.T) {
	srv, _, _, base := overviewServer(t)
	run := startOverviewRun(t, srv)
	w := overviewWatchOf(srv, base, run)
	if !srv.overviewStep(context.Background(), w, time.Now().Add(config.DefaultOverviewTimeout+time.Minute)) {
		t.Fatal("did not time out")
	}
	if got := getOverview(t, srv); got.State != OverviewFailed || !strings.Contains(got.Error, "no valid report within") {
		t.Fatalf("run %+v", got)
	}
}

func TestOverviewRefusals(t *testing.T) {
	srv, _, _, _ := overviewServer(t)
	wantCode(t, rpc(t, srv, "p", "overview.run", map[string]any{}), CodeNeedsOperator)
	wantCode(t, rpc(t, srv, "", "overview.get", map[string]any{}), CodeNotFound)
	wantCode(t, rpc(t, srv, "", "overview.run", map[string]any{"model": "x"}), CodeInvalidParams)
	srv.SetAgentChoice(config.AgentsConfig{}, func(context.Context, string) bool { return true })
	wantCode(t, rpc(t, srv, "", "overview.run", map[string]any{}), CodeInvalidParams)
}

func TestCleanStaleOverviewsFailsLeftoverRuns(t *testing.T) {
	srv, _, _, base := overviewServer(t)
	run := startOverviewRun(t, srv)
	// A new serve: nothing running in memory, the run still on disk.
	srv.overviewMu.Lock()
	srv.overview.running = nil
	srv.overviewMu.Unlock()
	srv.CleanStaleOverviews(context.Background())
	got := getOverview(t, srv)
	if got.ID != run.ID || got.State != OverviewFailed || !strings.HasSuffix(got.Error, ": serve restarted") || pathExists(filepath.Join(base, run.ID)) {
		t.Fatalf("run %+v", got)
	}
}
