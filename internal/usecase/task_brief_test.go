package usecase

import (
	"context"
	"strings"
	"testing"

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

const testBrief = "## Question\n\nDoes the parser accept nested lists?\n\n## Scope\n\ninternal/parse only.\n\n## Report must contain\n\nThe tests you ran.\n"

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
	if !strings.HasPrefix(body, "## Question\n") {
		t.Fatalf("task body starts %q, want the brief", body[:min(40, len(body))])
	}
	q, s, r, j := strings.Index(body, "## Question"), strings.Index(body, "## Scope"),
		strings.Index(body, "## Report must contain"), strings.Index(body, "JIRA description text.")
	if q >= s || s >= r || r >= j {
		t.Fatalf("order question %d scope %d report %d jira %d:\n%s", q, s, r, j, body)
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
	if got != domain.AimanScaffoldHeader+testBrief {
		t.Fatalf("task file = %q", got)
	}
}
