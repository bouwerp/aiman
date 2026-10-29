package usecase

import (
	"context"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/coordinator"
	"github.com/bouwerp/aiman/internal/domain"
)

// fileExec is a one-directory fake host: cat and WriteFile share files.
type fileExec struct {
	files map[string]string
}

func (f *fileExec) Execute(_ context.Context, cmd string) (string, error) {
	for path, body := range f.files {
		if strings.Contains(cmd, "cat ") && strings.Contains(cmd, path) {
			return "login noise\n" + taskBriefMarker + "\n" + body, nil
		}
	}
	if strings.Contains(cmd, "cat ") {
		return "login noise\n" + taskBriefMarker + "\n", nil
	}
	return "", nil
}

func (f *fileExec) WriteFile(_ context.Context, path string, content []byte) error {
	f.files[path] = string(content)
	return nil
}

const testBrief = "## Objective\n\nParse nested lists.\n\n## Owns\n\ninternal/parse only.\n\n## Report must contain\n\nThe tests you ran.\n"

// wrappedBrief is testBrief as the task file carries it.
const wrappedBrief = coordinator.BriefStartMarker + "\n" + testBrief + coordinator.BriefEndMarker + "\n"

func TestChildTaskStartsWithBrief(t *testing.T) {
	path := "/w/child/" + domain.AimanTaskFileName
	jira := domain.AimanScaffoldHeader + "---\n\n# Task\n\n## PROJ-1: Parser\n\nJIRA description text.\n"
	x := &fileExec{files: map[string]string{path: jira}}

	if err := WriteTaskBrief(context.Background(), x, "/w/child", testBrief); err != nil {
		t.Fatal(err)
	}
	got := x.files[path]
	body, ok := strings.CutPrefix(got, domain.AimanScaffoldHeader)
	if !ok {
		t.Fatalf("task file lost its scaffold header:\n%s", got)
	}
	if !strings.HasPrefix(body, wrappedBrief) {
		t.Fatalf("task body starts %q, want the brief", body[:min(40, len(body))])
	}
	if strings.Contains(got, coordinator.ChildGuidelines) {
		t.Fatalf("WriteTaskBrief added child guidelines:\n%s", got)
	}
	if end, j := strings.Index(body, coordinator.BriefEndMarker), strings.Index(body, "JIRA description text."); end < 0 || end >= j {
		t.Fatalf("brief end marker %d, jira %d:\n%s", end, j, body)
	}
	if strings.Count(got, domain.AimanScaffoldHeader) != 1 {
		t.Fatalf("scaffold header repeated:\n%s", got)
	}

	// Writing the same brief again leaves the file as it is.
	if err := WriteTaskBrief(context.Background(), x, "/w/child", testBrief); err != nil {
		t.Fatal(err)
	}
	if x.files[path] != got {
		t.Fatalf("second write changed the file:\n%s", x.files[path])
	}
}

func TestTaskBriefWithoutTaskFile(t *testing.T) {
	x := &fileExec{files: map[string]string{}}
	if err := WriteTaskBrief(context.Background(), x, "/w/child", testBrief); err != nil {
		t.Fatal(err)
	}
	got := x.files["/w/child/"+domain.AimanTaskFileName]
	if got != domain.AimanScaffoldHeader+wrappedBrief {
		t.Fatalf("task file = %q", got)
	}
}
