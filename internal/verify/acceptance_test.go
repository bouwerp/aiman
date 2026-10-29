package verify

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestTouchedProtected(t *testing.T) {
	const prot = "internal/parse/parse_test.go"
	tests := []struct {
		name  string
		patch string
		want  []string
	}{
		{"edit", "diff --git a/internal/parse/parse_test.go b/internal/parse/parse_test.go\n--- a/x\n", []string{prot}},
		{"rename away", "diff --git a/internal/parse/parse_test.go b/internal/parse/old_test.go\nsimilarity index 90%\n", []string{prot}},
		{"unrelated", "diff --git a/internal/parse/parse.go b/internal/parse/parse.go\n", nil},
		{"empty", "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := TouchedProtected(tc.patch, []string{prot})
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	if got := TouchedProtected("diff --git a/x b/x\n", nil); got != nil {
		t.Fatalf("no files: %v", got)
	}
}

func briefTask(acceptance string) string {
	return "# Task\n\n<!-- aiman:brief -->\n## Objective\n\nAdd the x variable to package a today.\n\n## Contract\n\nPackage a exports nothing new.\n\n## Owns\n\nsub/a.go\n\n" +
		"## Out of scope\n\nEverything else.\n\n## Acceptance tests\n\n" + acceptance + "\n\n## Report must contain\n\nCommands and exit codes.\n\n## Escalate when\n\nThe shape changes.\n<!-- /aiman:brief -->\n"
}

func finishWithTask(t *testing.T, task string) Review {
	t.Helper()
	r := newMRig(t, Config{})
	if err := os.WriteFile(filepath.Join(r.worker.WorkingDirectory, domain.AimanTaskFileName), []byte(task), 0o600); err != nil {
		t.Fatal(err)
	}
	rv, err := r.m.Start(context.Background(), StartRequest{WorkerID: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	running := r.waitStatus(t, rv.ID, StatusRunning)
	if err := os.WriteFile(filepath.Join(running.Worktree, running.Dir, domain.AimanReviewFileName), []byte("# Review\n\n"+CompleteMarker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	eventually(t, "finished", func() bool {
		got, _ := r.m.Status(rv.ID)
		return !got.Status.Active()
	})
	got, _ := r.m.Status(rv.ID)
	return got
}

func TestFinishDeniesProtectedEdit(t *testing.T) {
	got := finishWithTask(t, briefTask("covers `sub/a.go`"))
	if got.Status != StatusIncomplete || got.Required != 1 || len(got.Findings) != 1 {
		t.Fatalf("status %s required %d findings %+v", got.Status, got.Required, got.Findings)
	}
	if got.Findings[0].Title != "Acceptance test edited: sub/a.go" || got.Passed() {
		t.Fatalf("finding %+v", got.Findings[0])
	}
}

func TestFinishRunsAcceptanceCommands(t *testing.T) {
	got := finishWithTask(t, briefTask("`false` and `true` must hold for sub/other_test.go"))
	if got.Status != StatusComplete || got.Required != 1 {
		t.Fatalf("status %s required %d findings %+v", got.Status, got.Required, got.Findings)
	}
	if got.Findings[0].Title != "Acceptance test failed: false" || got.Passed() {
		t.Fatalf("finding %+v", got.Findings[0])
	}
	pass := finishWithTask(t, briefTask("`true` must hold for sub/other_test.go"))
	if !pass.Passed() {
		t.Fatalf("passing acceptance did not pass: %+v", pass)
	}
}

func TestFinishWithoutBriefIsUnchanged(t *testing.T) {
	got := finishWithTask(t, "Add x.\n")
	if !got.Passed() || len(got.Findings) != 0 || strings.Contains(got.Error, "acceptance") {
		t.Fatalf("%+v", got)
	}
}
