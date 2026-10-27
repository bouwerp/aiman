package verify

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTreeHashMatchesSnapshot(t *testing.T) {
	repo := newTestRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := runTestGit(t, repo, "status", "--porcelain")
	hash, err := TreeHash(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := TakeSnapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if hash != snap.Tree {
		t.Fatalf("TreeHash %s, snapshot tree %s", hash, snap.Tree)
	}
	if after := runTestGit(t, repo, "status", "--porcelain"); after != before {
		t.Fatalf("status changed: %q -> %q", before, after)
	}
}

func TestForTreeAndEnabled(t *testing.T) {
	r := newMRig(t, Config{BeforePR: true})
	if !r.m.BeforePR() {
		t.Fatal("BeforePR not reported")
	}
	rv := r.running(t)
	got, ok := r.m.ForTree("w1", rv.Tree)
	if !ok || got.ID != rv.ID {
		t.Fatalf("ForTree %+v %v", got, ok)
	}
	if _, ok := r.m.ForTree("w1", "0000000000000000000000000000000000000000"); ok {
		t.Fatal("ForTree matched another tree")
	}
}

func TestReproduceFirstIncompleteWhenBasePasses(t *testing.T) {
	on := true
	r := newMRig(t, Config{Reproduce: &on, TestCommand: "true"})
	rv, err := r.m.Start(context.Background(), StartRequest{WorkerID: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	done := r.waitStatus(t, rv.ID, StatusIncomplete)
	if done.Required != 1 || len(done.Findings) != 1 || done.Findings[0].Title != "not reproducible" ||
		done.Findings[0].Severity != SeverityRequired || done.Passed() {
		t.Fatalf("review %+v", done)
	}
	if n := len(r.sessions.createdConfigs()); n != 0 {
		t.Fatalf("a reviewer was spawned for an unreproduced bug: %d", n)
	}
	r.waitCleaned(t, rv.ID)
}

func TestReproduceFirstProceedsWhenBaseFails(t *testing.T) {
	on := true
	// a.go gains "var x" only in the worker's change, so the base fails.
	r := newMRig(t, Config{Reproduce: &on, TestCommand: "grep -q 'var x' a.go"})
	rv, err := r.m.Start(context.Background(), StartRequest{WorkerID: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	running := r.waitStatus(t, rv.ID, StatusRunning)
	writeReview(t, running, CompleteMarker)
	if done := r.waitStatus(t, rv.ID, StatusComplete); !done.Passed() {
		t.Fatalf("review %+v", done)
	}
}

func TestReproduceWithoutCommandIsSkipped(t *testing.T) {
	on := true
	r := newMRig(t, Config{Reproduce: &on, TestCommand: "-"})
	rv, err := r.m.Start(context.Background(), StartRequest{WorkerID: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	r.waitStatus(t, rv.ID, StatusRunning)
	if n := len(r.sessions.createdConfigs()); n != 1 {
		t.Fatalf("created %d", n)
	}
}
