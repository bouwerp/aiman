package verify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPassedRequiresEvidence(t *testing.T) {
	cases := []struct {
		name string
		r    Review
		want bool
	}{
		{"no command", Review{Status: StatusComplete}, false},
		{"exit 0", Review{Status: StatusComplete, Evidence: Evidence{Command: "make test"}}, true},
		{"exit 1", Review{Status: StatusComplete, Evidence: Evidence{Command: "make test", ExitCode: 1}}, false},
		{"required finding", Review{Status: StatusComplete, Required: 1, Evidence: Evidence{Command: "make test"}}, false},
		{"incomplete", Review{Status: StatusIncomplete, Evidence: Evidence{Command: "make test"}}, false},
	}
	for _, c := range cases {
		if got := c.r.Passed(); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestManagerRunsConfiguredCommand(t *testing.T) {
	r := newMRig(t, Config{TestCommand: "echo ok; pwd"})
	r.term.stopKills = true
	rv := r.running(t)
	writeReview(t, rv, CompleteMarker)
	done := r.waitStatus(t, rv.ID, StatusComplete)
	ev := done.Evidence
	if ev.Command != "echo ok; pwd" || ev.ExitCode != 0 || !strings.HasPrefix(ev.Output, "ok\n") || ev.TreeHash != rv.Tree || ev.RanAt.IsZero() {
		t.Fatalf("evidence %+v", ev)
	}
	if !strings.HasSuffix(strings.TrimSpace(ev.Output), "@evidence-"+rv.ID[:8]+"/sub") {
		t.Fatalf("command did not run in the evidence worktree's working dir: %q", ev.Output)
	}
	if !done.Passed() {
		t.Fatal("a complete review with passing evidence should pass")
	}
	stored, err := r.store.Get(rv.ID)
	if err != nil || stored.Evidence.Command != ev.Command {
		t.Fatalf("evidence not persisted: %+v %v", stored.Evidence, err)
	}
}

func TestManagerRunsInSecondWorktree(t *testing.T) {
	r := newMRig(t, Config{TestCommand: "touch evidence-was-here"})
	rv := r.running(t)
	writeReview(t, rv, CompleteMarker)
	r.waitStatus(t, rv.ID, StatusComplete)
	if _, err := os.Stat(filepath.Join(rv.Worktree, rv.Dir, "evidence-was-here")); !os.IsNotExist(err) {
		t.Fatalf("command ran in the reviewer's worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(r.worker.WorktreePath), "repo@evidence-"+rv.ID[:8])); !os.IsNotExist(err) {
		t.Fatalf("evidence worktree left behind: %v", err)
	}
}

func TestFailingCommandIsNotAPass(t *testing.T) {
	r := newMRig(t, Config{TestCommand: "echo broken >&2; exit 3"})
	rv := r.running(t)
	writeReview(t, rv, CompleteMarker)
	done := r.waitStatus(t, rv.ID, StatusComplete)
	if done.Evidence.ExitCode != 3 || !strings.Contains(done.Evidence.Output, "broken") || done.Passed() {
		t.Fatalf("evidence %+v passed %v", done.Evidence, done.Passed())
	}
}

func TestMissingCommandIsIncomplete(t *testing.T) {
	r := newMRig(t, Config{TestCommand: "-"})
	rv := r.running(t)
	writeReview(t, rv, CompleteMarker)
	done := r.waitStatus(t, rv.ID, StatusIncomplete)
	if !strings.Contains(done.Error, "verify.test_command") || done.Passed() {
		t.Fatalf("review %+v", done)
	}
}

func TestEvidenceOutputKeepsLast200Lines(t *testing.T) {
	r := newMRig(t, Config{TestCommand: "seq 1 300"})
	rv := r.running(t)
	writeReview(t, rv, CompleteMarker)
	done := r.waitStatus(t, rv.ID, StatusComplete)
	lines := strings.Split(strings.TrimSuffix(done.Evidence.Output, "\n"), "\n")
	if len(lines) != 200 || lines[0] != "101" || lines[199] != "300" {
		t.Fatalf("got %d lines, first %q", len(lines), lines[0])
	}
}
