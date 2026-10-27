package verify

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestBuildBrief(t *testing.T) {
	in := BriefInput{
		WorkerName: "worker", BaseRef: "origin/main", Commit: "abc123",
		Diff: Diff{Stat: " a.go | 2 ++", Patch: "diff --git a/a.go b/a.go\n+ok\n", MergeBase: "base123", Truncated: true},
		Task: "Fix the parser", Todos: []domain.TodoItem{{ID: "t3", State: domain.TodoActive, Text: "Run tests"}},
	}
	got := BuildBrief(in)
	for _, want := range []string{
		"DO NOT COMMIT", "Fix the parser", "t3 ▸ Run tests", " a.go | 2 ++",
		"diff --git a/a.go b/a.go", "git diff base123 abc123",
		"## [REQUIRED] <short title>", CompleteMarker,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("brief missing %q:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(got), CompleteMarker) {
		t.Fatalf("marker is not the last line:\n%s", got)
	}
}
