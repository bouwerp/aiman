package handoverchild

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/handover"
	"github.com/bouwerp/aiman/internal/infra/config"
)

func TestSuccess(t *testing.T) {
	r := newRig(t)
	id := r.start(t, "restart")
	if !r.m.Active("w1") {
		t.Fatal("run not active after Start")
	}
	run := r.waitRunning(t, id)

	cfgs := r.sessions.createdConfigs()
	if len(cfgs) != 1 {
		t.Fatalf("created %d children", len(cfgs))
	}
	cfg := cfgs[0]
	if cfg.Role != domain.SessionRoleHandover || cfg.ParentID != "w1" || !cfg.AdHoc || !cfg.PromptFree ||
		cfg.PreparedWorktree != run.Dir || cfg.Agent == nil || cfg.Agent.Command != "claude" {
		t.Fatalf("child config %+v", cfg)
	}
	if want := "Read " + filepath.Join(run.Dir, handover.BriefFile) + " and follow it."; cfg.InitialPrompt != want {
		t.Fatalf("prompt %q, want %q", cfg.InitialPrompt, want)
	}
	if !filepath.IsAbs(run.Dir) || filepath.Dir(run.Dir) != r.base {
		t.Fatalf("scratch dir %q not under %q", run.Dir, r.base)
	}
	if _, err := os.Stat(filepath.Join(run.Dir, ".git")); err != nil {
		t.Fatalf("scratch not git init'ed: %v", err)
	}
	brief, _ := os.ReadFile(filepath.Join(run.Dir, handover.BriefFile))
	if !strings.Contains(string(brief), "/tmp/transcript.jsonl") || !strings.Contains(string(brief), r.worker.WorktreePath) {
		t.Fatalf("brief:\n%s", brief)
	}
	mech, _ := os.ReadFile(filepath.Join(run.Dir, handover.MechanicalFile))
	if !bytes.Equal(mech, mechanicalRender()) {
		t.Fatalf("mechanical.md %q", mech)
	}
	if run.Agent != "claude" || run.Transcript != "/tmp/transcript.jsonl" {
		t.Fatalf("run %+v", run)
	}

	writeOutputs(t, run.Dir, validChildHandover(), childMemories)
	done := r.waitCleaned(t, id)
	if done.Status != StatusComplete || !done.Applied || done.Candidates != 1 {
		t.Fatalf("finished run %+v", done)
	}
	if r.m.Active("w1") {
		t.Fatal("run still active")
	}
	if got := r.workerFile(t); got != domain.AimanScaffoldHeader+validChildHandover() {
		t.Fatalf("worker handover:\n%s", got)
	}
	puts := r.ctx.all()
	if len(puts) != 1 || puts[0].ID != "handover-w1" || puts[0].Body != validChildHandover() ||
		puts[0].Type != domain.ContextTypeHandover || puts[0].Namespace != domain.ContextNSGroup || puts[0].Key != "g" {
		t.Fatalf("context puts %+v", puts)
	}
	jobs := r.jobs.all()
	if len(jobs) != 1 || len(jobs[0].Candidates) != 1 || jobs[0].Source != validChildHandover() ||
		jobs[0].SessionID != "w1" || jobs[0].Reason != "restart" || jobs[0].Repo != "o/r" || jobs[0].Group != "g" {
		t.Fatalf("jobs %+v", jobs)
	}
	if notes := r.steer.all(); len(notes) != 1 || notes[0] != (steered{"w1", HandoverRefreshedNote}) {
		t.Fatalf("steer %+v", notes)
	}
	if got := r.sessions.forgottenIDs(); len(got) != 1 || got[0] != "child-1" {
		t.Fatalf("forgotten %v", got)
	}
	if got := r.term.stoppedIDs(); len(got) != 1 || got[0] != "child-1" {
		t.Fatalf("stopped %v", got)
	}
	if _, err := os.Stat(run.Dir); !os.IsNotExist(err) {
		t.Fatalf("scratch dir left: %v", err)
	}
	if got, _ := r.m.LastApplied("w1"); got != "sha-1" {
		t.Fatalf("LastApplied %q", got)
	}
}

func TestLedgerStartThenFinish(t *testing.T) {
	r := newRig(t)
	id := r.start(t, "archive")
	run := r.waitRunning(t, id)
	writeOutputs(t, run.Dir, validChildHandover(), childMemories)
	r.waitCleaned(t, id)
	ev := r.ledger.all()
	if len(ev) != 2 || ev[0].kind != domain.EventHandoverChildStarted || ev[1].kind != domain.EventHandoverChildFinished {
		t.Fatalf("events %+v", ev)
	}
	if ev[0].session != "w1" || ev[0].payload["run"] != id || ev[0].payload["agent"] != "claude" || ev[0].payload["reason"] != "archive" {
		t.Fatalf("started %+v", ev[0])
	}
	if ev[1].payload["status"] != string(StatusComplete) || ev[1].payload["candidates"] != 1 {
		t.Fatalf("finished %+v", ev[1])
	}
	// archive success queues no refreshed note.
	if notes := r.steer.all(); len(notes) != 0 {
		t.Fatalf("steer %+v", notes)
	}
}

func TestTimeout(t *testing.T) {
	r := newRig(t)
	id := r.start(t, "restart")
	r.waitRunning(t, id)
	r.clock.advance(11 * time.Minute)
	done := r.waitCleaned(t, id)
	if done.Status != StatusTimedOut || done.Applied {
		t.Fatalf("run %+v", done)
	}
	if got := r.term.stoppedIDs(); len(got) != 1 || got[0] != "child-1" {
		t.Fatalf("stopped %v", got)
	}
	assertFallback(t, r)
}

func TestExitWithoutMarker(t *testing.T) {
	r := newRig(t)
	id := r.start(t, "restart")
	run := r.waitRunning(t, id)
	// Outputs without the done marker do not count.
	if err := os.WriteFile(filepath.Join(run.Dir, handover.HandoverFile), []byte(validChildHandover()), 0o600); err != nil {
		t.Fatal(err)
	}
	r.term.kill(run.ChildID)
	done := r.waitCleaned(t, id)
	if done.Status != StatusIncomplete {
		t.Fatalf("run %+v", done)
	}
	assertFallback(t, r)
}

// assertFallback checks a failed run: mechanical file kept, mechanical note
// and job without candidates, no steer.
func assertFallback(t *testing.T, r *rig) {
	t.Helper()
	if got := r.workerFile(t); got != string(mechanicalRender()) {
		t.Fatalf("worker handover changed:\n%s", got)
	}
	jobs := r.jobs.all()
	if len(jobs) != 1 || len(jobs[0].Candidates) != 0 || jobs[0].Source != mechanicalBody {
		t.Fatalf("jobs %+v", jobs)
	}
	puts := r.ctx.all()
	if len(puts) != 1 || puts[0].Body != mechanicalBody || puts[0].Abstract != "mechanical abstract" {
		t.Fatalf("context %+v", puts)
	}
	if notes := r.steer.all(); len(notes) != 0 {
		t.Fatalf("steer %+v", notes)
	}
	if got, _ := r.m.LastApplied("w1"); got != "" {
		t.Fatalf("LastApplied %q after a fallback", got)
	}
}

func TestInvalidHandoverValidCandidates(t *testing.T) {
	r := newRig(t)
	id := r.start(t, "restart")
	run := r.waitRunning(t, id)
	writeOutputs(t, run.Dir, "# Handover\n\n## Goal\n\nonly a goal\n", childMemories)
	done := r.waitCleaned(t, id)
	if done.Status != StatusComplete || done.Applied || done.Error == "" {
		t.Fatalf("run %+v", done)
	}
	if got := r.workerFile(t); got != string(mechanicalRender()) {
		t.Fatalf("worker handover changed:\n%s", got)
	}
	jobs := r.jobs.all()
	if len(jobs) != 1 || len(jobs[0].Candidates) != 1 || jobs[0].Source != mechanicalBody {
		t.Fatalf("jobs %+v", jobs)
	}
	if notes := r.steer.all(); len(notes) != 0 {
		t.Fatalf("an unapplied handover queued %+v", notes)
	}
}

func TestBadMemoriesGiveNoCandidates(t *testing.T) {
	r := newRig(t)
	id := r.start(t, "archive")
	run := r.waitRunning(t, id)
	writeOutputs(t, run.Dir, validChildHandover(), "{not json")
	done := r.waitCleaned(t, id)
	if !done.Applied || done.Candidates != 0 {
		t.Fatalf("run %+v", done)
	}
	if jobs := r.jobs.all(); len(jobs) != 1 || len(jobs[0].Candidates) != 0 || jobs[0].Source != validChildHandover() {
		t.Fatalf("jobs %+v", jobs)
	}
}

func TestSecondStartAbsorbed(t *testing.T) {
	r := newRig(t)
	id := r.start(t, "archive")
	r.waitRunning(t, id)
	got, started, err := r.m.Start(context.Background(), StartRequest{Worker: r.worker, Reason: "pre_compact", Mechanical: mechanicalRender()})
	if err != nil || started || got != id {
		t.Fatalf("second start: id %q started %v err %v", got, started, err)
	}
	run, _ := r.store.Get(id)
	if len(run.LaterReasons) != 1 || run.LaterReasons[0] != "pre_compact" {
		t.Fatalf("later reasons %v", run.LaterReasons)
	}
	if n := len(r.sessions.createdConfigs()); n != 1 {
		t.Fatalf("created %d children", n)
	}
}

func TestHoldAndRelease(t *testing.T) {
	r := newRig(t)
	if r.m.HoldPointer("w1", "pointer") {
		t.Fatal("held a pointer with no run")
	}
	id := r.start(t, "pre_compact")
	run := r.waitRunning(t, id)
	if !r.m.HoldPointer("w1", "compaction pointer") {
		t.Fatal("pointer not held during a run")
	}
	if notes := r.steer.all(); len(notes) != 0 {
		t.Fatalf("steer before finish %+v", notes)
	}
	writeOutputs(t, run.Dir, validChildHandover(), childMemories)
	done := r.waitCleaned(t, id)
	if !done.PointerReleased {
		t.Fatalf("run %+v", done)
	}
	if notes := r.steer.all(); len(notes) != 1 || notes[0] != (steered{"w1", "compaction pointer"}) {
		t.Fatalf("steer %+v", notes)
	}
	if r.m.HoldPointer("w1", "late") {
		t.Fatal("held a pointer after the run ended")
	}
}

func TestHeldPointerReplacesRefreshedNote(t *testing.T) {
	r := newRig(t)
	id := r.start(t, "restart")
	run := r.waitRunning(t, id)
	r.m.HoldPointer("w1", "restart pointer")
	writeOutputs(t, run.Dir, validChildHandover(), childMemories)
	r.waitCleaned(t, id)
	if notes := r.steer.all(); len(notes) != 1 || notes[0].text != "restart pointer" {
		t.Fatalf("steer %+v", notes)
	}
}

func TestWorkerGoneAtFinish(t *testing.T) {
	r := newRig(t)
	id := r.start(t, "pre_compact")
	run := r.waitRunning(t, id)
	r.m.HoldPointer("w1", "compaction pointer")
	r.sessions.remove("w1")
	writeOutputs(t, run.Dir, validChildHandover(), childMemories)
	done := r.waitCleaned(t, id)
	if done.Applied {
		t.Fatalf("run %+v", done)
	}
	if got := r.workerFile(t); got != string(mechanicalRender()) {
		t.Fatalf("worker file written for a gone session:\n%s", got)
	}
	if notes := r.steer.all(); len(notes) != 0 {
		t.Fatalf("steer %+v", notes)
	}
	if puts := r.ctx.all(); len(puts) != 1 || puts[0].Body != validChildHandover() {
		t.Fatalf("context %+v", puts)
	}
	if jobs := r.jobs.all(); len(jobs) != 1 || len(jobs[0].Candidates) != 1 {
		t.Fatalf("jobs %+v", jobs)
	}
}

func TestNoInstalledAgentFails(t *testing.T) {
	r := newRig(t)
	r.m.Close()
	r.m = NewManager(Deps{
		Store: r.store, Sessions: r.sessions, Terminal: r.term, Steer: r.steer, Ledger: r.ledger,
		Context: r.ctx, Jobs: r.jobs, Installed: func(string) bool { return false },
		Candidates: func(w string) []config.PreferredAgent { return []config.PreferredAgent{{Agent: w}} }, BaseDir: r.base, Now: r.clock.Now, Poll: time.Millisecond,
	})
	t.Cleanup(r.m.Close)
	id := r.start(t, "archive")
	done := r.waitCleaned(t, id)
	if done.Status != StatusFailed || done.Error == "" {
		t.Fatalf("run %+v", done)
	}
	if n := len(r.sessions.createdConfigs()); n != 0 {
		t.Fatalf("created %d children", n)
	}
	assertFallback(t, r)
	if ev := r.ledger.all(); len(ev) != 1 || ev[0].kind != domain.EventHandoverChildFinished {
		t.Fatalf("events %+v", ev)
	}
}

func TestStartRejectsHiddenSession(t *testing.T) {
	r := newRig(t)
	w := r.worker
	w.Role = domain.SessionRoleHandover
	if _, _, err := r.m.Start(context.Background(), StartRequest{Worker: w, Reason: "archive"}); !errors.Is(err, ErrHiddenSession) {
		t.Fatalf("err %v", err)
	}
}

func TestResumeRunning(t *testing.T) {
	r := newRig(t)
	dir := filepath.Join(r.base, "run-running")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	child := domain.Session{ID: "child-9", Role: domain.SessionRoleHandover}
	r.sessions.put(child)
	r.putRun(t, Run{ID: "run-running", SessionID: "w1", ChildID: "child-9", Agent: "claude", Reason: "archive",
		Dir: dir, Mechanical: string(mechanicalRender()), Group: "g", Label: "w1", Status: StatusRunning, StartedAt: r.clock.Now()})
	r.m.Resume(context.Background())
	if !r.m.Active("w1") {
		t.Fatal("resumed running run not active")
	}
	writeOutputs(t, dir, validChildHandover(), childMemories)
	done := r.waitCleaned(t, "run-running")
	if done.Status != StatusComplete || !done.Applied {
		t.Fatalf("run %+v", done)
	}
	if got := r.sessions.forgottenIDs(); len(got) != 1 || got[0] != "child-9" {
		t.Fatalf("forgotten %v", got)
	}
}

func TestResumeStarting(t *testing.T) {
	r := newRig(t)
	dir := filepath.Join(r.base, "run-starting")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	r.putRun(t, Run{ID: "run-starting", SessionID: "w1", Reason: "pre_compact", Dir: dir,
		Mechanical: string(mechanicalRender()), Label: "w1", Abstract: "mechanical abstract", Group: "g",
		HeldPointer: "compaction pointer", Status: StatusStarting, StartedAt: r.clock.Now()})
	r.m.Resume(context.Background())
	done := r.waitCleaned(t, "run-starting")
	if done.Status != StatusFailed {
		t.Fatalf("run %+v", done)
	}
	if jobs := r.jobs.all(); len(jobs) != 1 || jobs[0].Source != mechanicalBody {
		t.Fatalf("jobs %+v", jobs)
	}
	if notes := r.steer.all(); len(notes) != 1 || notes[0].text != "compaction pointer" {
		t.Fatalf("held pointer not released: %+v", notes)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("scratch left: %v", err)
	}
}

func TestResumeFinishedNotCleaned(t *testing.T) {
	r := newRig(t)
	dir := filepath.Join(r.base, "run-done")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	r.sessions.put(domain.Session{ID: "child-7", Role: domain.SessionRoleHandover})
	r.putRun(t, Run{ID: "run-done", SessionID: "w1", ChildID: "child-7", Reason: "archive", Dir: dir,
		Status: StatusComplete, StartedAt: r.clock.Now(), FinishedAt: r.clock.Now()})
	r.m.Resume(context.Background())
	r.waitCleaned(t, "run-done")
	if got := r.sessions.forgottenIDs(); len(got) != 1 || got[0] != "child-7" {
		t.Fatalf("forgotten %v", got)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("scratch left: %v", err)
	}
	if jobs := r.jobs.all(); len(jobs) != 0 {
		t.Fatalf("a finished run enqueued again: %+v", jobs)
	}
}

func TestCleanupRefusesDirOutsideBase(t *testing.T) {
	r := newRig(t)
	outside := t.TempDir()
	r.putRun(t, Run{ID: "run-x", SessionID: "w1", Dir: outside, Status: StatusFailed, StartedAt: r.clock.Now()})
	r.m.Resume(context.Background())
	time.Sleep(20 * time.Millisecond)
	r.m.Close()
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("removed a dir outside the base: %v", err)
	}
}

func (r *rig) putRun(t *testing.T, run Run) {
	t.Helper()
	if err := r.store.Put(run); err != nil {
		t.Fatal(err)
	}
}

// The picked worker-tier entry brings its model and effort: a handover child
// runs the cheap model the tier names, not the role default.
func TestPickAgentKeepsTierEntryModel(t *testing.T) {
	m := NewManager(Deps{
		Installed: func(a string) bool { return a == "pi" },
		Candidates: func(string) []config.PreferredAgent {
			return []config.PreferredAgent{
				{Agent: "claude"},
				{Agent: "pi", Model: "openrouter/z-ai/glm-5.3"},
				{Agent: "pi", Model: "openrouter/deepseek/deepseek-v4.1-flash"},
			}
		},
		BaseDir: t.TempDir(),
	})
	t.Cleanup(m.Close)
	got := m.pickAgent("claude")
	want := config.PreferredAgent{Agent: "pi", Model: "openrouter/z-ai/glm-5.3"}
	if got != want {
		t.Fatalf("pickAgent = %+v, want %+v", got, want)
	}
	if got := (NewManager(Deps{Installed: func(string) bool { return false }})).pickAgent("claude"); got.Agent != "" {
		t.Fatalf("nothing installed must pick nothing, got %+v", got)
	}
}
