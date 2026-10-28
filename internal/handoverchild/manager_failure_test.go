package handoverchild

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/handover"
)

var errLocked = errors.New("database is locked")

// settle gives the watcher several polls to act on a state it must ignore.
func settle() { time.Sleep(30 * time.Millisecond) }

func TestFinishWaitsOutTransientWorkerLookup(t *testing.T) {
	r := newRig(t)
	id := r.start(t, "restart")
	run := r.waitRunning(t, id)
	r.m.HoldPointer("w1", "restart pointer")
	r.sessions.setGetErr("w1", errLocked)
	writeOutputs(t, run.Dir, validChildHandover(), childMemories)
	settle()

	got, _ := r.store.Get(id)
	if got.Status != StatusRunning || got.PointerReleased {
		t.Fatalf("run after a lookup error %+v, want it still running", got)
	}
	if jobs, puts := r.jobs.all(), r.ctx.all(); len(jobs) != 0 || len(puts) != 0 {
		t.Fatalf("jobs %+v puts %+v before the worker could be read", jobs, puts)
	}

	r.sessions.setGetErr("w1", nil)
	done := r.waitCleaned(t, id)
	if done.Status != StatusComplete || !done.Applied || !done.PointerReleased {
		t.Fatalf("run %+v", done)
	}
	if notes := r.steer.all(); len(notes) != 1 || notes[0].text != "restart pointer" {
		t.Fatalf("steer %+v", notes)
	}
}

func TestChildLookupErrorKeepsRunRunning(t *testing.T) {
	r := newRig(t)
	id := r.start(t, "archive")
	run := r.waitRunning(t, id)
	r.sessions.setGetErr(run.ChildID, errLocked)
	settle()
	if got, _ := r.store.Get(id); got.Status != StatusRunning {
		t.Fatalf("run %+v ended on a lookup error", got)
	}
	r.sessions.setGetErr(run.ChildID, nil)
	r.term.kill(run.ChildID)
	if done := r.waitCleaned(t, id); done.Status != StatusIncomplete {
		t.Fatalf("run %+v", done)
	}
}

func TestFinishAppliesOnCancelledContext(t *testing.T) {
	r := newRig(t)
	id := r.start(t, "archive")
	run := r.waitRunning(t, id)
	r.m.Close()
	writeOutputs(t, run.Dir, validChildHandover(), childMemories)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.m.finish(ctx, id, StatusComplete, "")

	got, _ := r.store.Get(id)
	if got.Status != StatusComplete || !got.Applied {
		t.Fatalf("run %+v", got)
	}
	if jobs := r.jobs.all(); len(jobs) != 1 || len(jobs[0].Candidates) != 1 {
		t.Fatalf("jobs %+v", jobs)
	}
	if puts := r.ctx.all(); len(puts) != 1 || puts[0].Body != validChildHandover() {
		t.Fatalf("puts %+v", puts)
	}
}

func TestResumeKeepsPointerOnTransientLookup(t *testing.T) {
	r := newRig(t)
	dir := filepath.Join(r.base, "run-held")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	r.putRun(t, Run{ID: "run-held", SessionID: "w1", Reason: "pre_compact", Dir: dir,
		HeldPointer: "compaction pointer", Status: StatusComplete, StartedAt: r.clock.Now(), FinishedAt: r.clock.Now()})
	r.sessions.setGetErr("w1", errLocked)
	r.m.Resume(context.Background())
	settle()
	if got, _ := r.store.Get("run-held"); got.PointerReleased || got.CleanedUp {
		t.Fatalf("run %+v released or cleaned while the worker could not be read", got)
	}
	if notes := r.steer.all(); len(notes) != 0 {
		t.Fatalf("steer %+v", notes)
	}
	r.sessions.setGetErr("w1", nil)
	if done := r.waitCleaned(t, "run-held"); !done.PointerReleased {
		t.Fatalf("run %+v", done)
	}
	if notes := r.steer.all(); len(notes) != 1 || notes[0].text != "compaction pointer" {
		t.Fatalf("steer %+v", notes)
	}
}

func TestMemoryJobIDIsRunID(t *testing.T) {
	r := newRig(t)
	id := r.start(t, "archive")
	run := r.waitRunning(t, id)
	writeOutputs(t, run.Dir, validChildHandover(), childMemories)
	r.waitCleaned(t, id)
	if jobs := r.jobs.all(); len(jobs) != 1 || jobs[0].ID != id {
		t.Fatalf("jobs %+v, want one with the run id", jobs)
	}
}

func TestWorkerGoneNoteUsesIssueKey(t *testing.T) {
	r := newRig(t)
	r.worker.Group, r.worker.IssueKey = "", "PROJ-7"
	r.sessions.put(r.worker)
	id := r.start(t, "archive")
	run := r.waitRunning(t, id)
	r.sessions.remove("w1")
	writeOutputs(t, run.Dir, validChildHandover(), childMemories)
	r.waitCleaned(t, id)
	puts := r.ctx.all()
	if len(puts) != 1 || puts[0].Namespace != domain.ContextNSGroup || puts[0].Key != "PROJ-7" {
		t.Fatalf("puts %+v, want the issue-key namespace serve uses", puts)
	}
}

func TestJoinReplacesMechanical(t *testing.T) {
	r := newRig(t)
	id := r.start(t, "restart")
	run := r.waitRunning(t, id)
	newerBody := "# Handover: w1b\n\nnewer body\n"
	got, started, err := r.m.Start(context.Background(), StartRequest{
		Worker: r.worker, Reason: "pre_compact", Mechanical: []byte(domain.AimanScaffoldHeader + newerBody),
		MechanicalSHA: "sha-2", Label: "w1b", Abstract: "newer abstract",
	})
	if err != nil || started || got != id {
		t.Fatalf("join: id %q started %v err %v", got, started, err)
	}
	r.term.kill(run.ChildID)
	r.waitCleaned(t, id)
	puts := r.ctx.all()
	if len(puts) != 1 || puts[0].Body != newerBody || puts[0].Abstract != "newer abstract" || puts[0].Title != "Handover: w1b" {
		t.Fatalf("puts %+v, want the newer mechanical", puts)
	}
	if jobs := r.jobs.all(); len(jobs) != 1 || jobs[0].Source != newerBody || jobs[0].Label != "w1b" {
		t.Fatalf("jobs %+v", jobs)
	}
	if run, _ := r.store.Get(id); run.MechanicalSHA != "sha-2" {
		t.Fatalf("mechanical sha %q", run.MechanicalSHA)
	}
}

func TestFinishPrunesOlderRuns(t *testing.T) {
	r := newRig(t)
	var ids []string
	for range 3 {
		id := r.start(t, "archive")
		run := r.waitRunning(t, id)
		writeOutputs(t, run.Dir, validChildHandover(), childMemories)
		r.waitCleaned(t, id)
		ids = append(ids, id)
		r.clock.advance(time.Minute)
	}
	// Pruning follows cleanup, so the last run is cleaned before it prunes.
	deadline := time.Now().Add(5 * time.Second)
	for {
		runs, err := r.store.List()
		if err == nil && len(runs) == 1 && runs[0].ID == ids[2] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("runs %d (%v), want only the newest", len(runs), err)
		}
		time.Sleep(time.Millisecond)
	}
	if mech, _ := r.m.LastApplied("w1"); mech != "sha-1" {
		t.Fatalf("LastApplied mechanical %q", mech)
	}
}

func TestResumePrunesCleanedRuns(t *testing.T) {
	r := newRig(t)
	t0 := r.clock.Now()
	outside := t.TempDir()
	r.putRun(t, Run{ID: "run-a", SessionID: "w1", Applied: true, CleanedUp: true, Status: StatusComplete, StartedAt: t0})
	r.putRun(t, Run{ID: "run-b", SessionID: "w1", CleanedUp: true, Status: StatusFailed, StartedAt: t0.Add(time.Minute)})
	r.putRun(t, Run{ID: "run-c", SessionID: "w2", CleanedUp: true, Status: StatusComplete, StartedAt: t0})
	// Never cleaned: its directory is outside the base, so cleanup refuses it.
	r.putRun(t, Run{ID: "run-d", SessionID: "w1", Dir: outside, Status: StatusFailed, StartedAt: t0.Add(-time.Minute)})
	r.m.Resume(context.Background())
	runs, err := r.store.List()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, run := range runs {
		got = append(got, run.ID)
	}
	if strings.Join(got, ",") != "run-b,run-c,run-d" {
		t.Fatalf("runs %v, want run-b,run-c,run-d", got)
	}
}

func TestResumeStartingCleansOrphanChild(t *testing.T) {
	r := newRig(t)
	id := "abcdef12-0000-4000-8000-000000000000"
	dir := filepath.Join(r.base, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	r.sessions.put(domain.Session{ID: "child-orphan", ParentID: "w1", Role: domain.SessionRoleHandover,
		Branch: "handover-abcdef12", WorktreePath: dir})
	r.sessions.put(domain.Session{ID: "child-other", ParentID: "w1", Role: domain.SessionRoleHandover,
		Branch: "handover-99999999", WorktreePath: filepath.Join(r.base, "other")})
	r.sessions.put(domain.Session{ID: "worker-child", ParentID: "w1", Branch: "handover-abcdef12", WorktreePath: dir})
	r.putRun(t, Run{ID: id, SessionID: "w1", Reason: "archive", Dir: dir, Mechanical: string(mechanicalRender()),
		Status: StatusStarting, StartedAt: r.clock.Now()})
	r.m.Resume(context.Background())
	done := r.waitCleaned(t, id)
	if done.ChildID != "child-orphan" || done.Status != StatusFailed {
		t.Fatalf("run %+v", done)
	}
	if got := r.sessions.forgottenIDs(); len(got) != 1 || got[0] != "child-orphan" {
		t.Fatalf("forgotten %v", got)
	}
	if got := r.term.stoppedIDs(); len(got) != 1 || got[0] != "child-orphan" {
		t.Fatalf("stopped %v", got)
	}
}

func TestLastAppliedFingerprintsWorkerFile(t *testing.T) {
	r := newRig(t)
	id := r.start(t, "archive")
	run := r.waitRunning(t, id)
	writeOutputs(t, run.Dir, validChildHandover(), childMemories)
	r.waitCleaned(t, id)
	sum := sha256.Sum256([]byte(r.workerFile(t)))
	mech, applied := r.m.LastApplied("w1")
	if mech != "sha-1" || applied != hex.EncodeToString(sum[:]) {
		t.Fatalf("LastApplied %q %q, want sha-1 and the worker file sha", mech, applied)
	}
}

func TestOversizedMemoriesGiveNoCandidates(t *testing.T) {
	r := newRig(t)
	id := r.start(t, "archive")
	run := r.waitRunning(t, id)
	big := `{"candidates":[{"type":"decision","title":"t","abstract":"a","body":"` +
		strings.Repeat("x", handover.MaxChildMemoriesBytes) + `"}]}`
	writeOutputs(t, run.Dir, validChildHandover(), big)
	if done := r.waitCleaned(t, id); done.Candidates != 0 || !done.Applied {
		t.Fatalf("run %+v, want the oversized memories ignored", done)
	}
}
