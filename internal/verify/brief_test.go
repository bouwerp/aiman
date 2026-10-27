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

func TestBuildBriefIncludesRubricAndDoneWhen(t *testing.T) {
	got := BuildBrief(BriefInput{
		WorkerName: "worker", BaseRef: "origin/main", Commit: "abc123",
		Rubric:   []RubricItem{{Path: "/r/.aiman/checklists/go.md", Body: "- [ ] wrap errors"}},
		DoneWhen: []string{"tests added"},
	})
	for _, want := range []string{
		"## Review rubric", "### /r/.aiman/checklists/go.md", "- [ ] wrap errors",
		"## Done-when conditions", "- tests added",
		"For every condition that does not hold in this snapshot, write a finding `## [REQUIRED] Done-when not met: <condition>` explaining what is missing.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("brief missing %q:\n%s", want, got)
		}
	}
	todos := strings.Index(got, "## Worker to-do list")
	rubric := strings.Index(got, "## Review rubric")
	conds := strings.Index(got, "## Done-when conditions")
	diff := strings.Index(got, "## Diff")
	if todos >= rubric || rubric >= conds || conds >= diff {
		t.Fatalf("sections out of order:\n%s", got)
	}
}

func TestBuildBriefNamesUnreadableChecklist(t *testing.T) {
	got := BuildBrief(BriefInput{Rubric: []RubricItem{{Path: "/gone.md"}}})
	if !strings.Contains(got, "### /gone.md\n\nnot readable") {
		t.Fatalf("unreadable checklist not named:\n%s", got)
	}
}

func TestBuildBriefOmitsEmptyKit(t *testing.T) {
	got := BuildBrief(BriefInput{WorkerName: "worker", BaseRef: "origin/main", Commit: "abc123", Task: "Fix it"})
	for _, unwanted := range []string{"## Review rubric", "## Done-when conditions"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("brief has %q:\n%s", unwanted, got)
		}
	}
}
