package ciwatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParsePRView(t *testing.T) {
	pr, err := ParsePRView(readFixture(t, "pr_view.json"))
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 42 || pr.HeadSHA != "abc123" || pr.State != "OPEN" {
		t.Fatalf("pr %+v", pr)
	}
	want := []Check{
		{Name: "lint", Conclusion: ConclusionFailure, DetailsURL: "https://github.com/o/r/actions/runs/111/job/222", JobID: 222},
		{Name: "test", Conclusion: ConclusionPending, DetailsURL: "https://github.com/o/r/actions/runs/111/job/223", JobID: 223},
		{Name: "build", Conclusion: ConclusionSuccess, DetailsURL: "https://github.com/o/r/actions/runs/111/job/224", JobID: 224},
		{Name: "ci/ext", Conclusion: ConclusionFailure, DetailsURL: "https://ci.example/9"},
	}
	if len(pr.Checks) != len(want) {
		t.Fatalf("checks %+v", pr.Checks)
	}
	for i := range want {
		if pr.Checks[i] != want[i] {
			t.Errorf("check %d: got %+v, want %+v", i, pr.Checks[i], want[i])
		}
	}
}

func TestConclusionMapping(t *testing.T) {
	cases := []struct {
		typename, status, conclusion, state string
		want                                Conclusion
	}{
		{"CheckRun", "QUEUED", "", "", ConclusionPending},
		{"CheckRun", "COMPLETED", "TIMED_OUT", "", ConclusionFailure},
		{"CheckRun", "COMPLETED", "STARTUP_FAILURE", "", ConclusionFailure},
		{"CheckRun", "COMPLETED", "NEUTRAL", "", ConclusionSuccess},
		{"CheckRun", "COMPLETED", "SKIPPED", "", ConclusionSuccess},
		{"CheckRun", "COMPLETED", "CANCELLED", "", ConclusionOther},
		{"CheckRun", "COMPLETED", "ACTION_REQUIRED", "", ConclusionOther},
		{"StatusContext", "", "", "FAILURE", ConclusionFailure},
		{"StatusContext", "", "", "SUCCESS", ConclusionSuccess},
		{"StatusContext", "", "", "PENDING", ConclusionPending},
		{"StatusContext", "", "", "EXPECTED", ConclusionPending},
	}
	for _, c := range cases {
		raw := fmt.Sprintf(`{"number":1,"headRefOid":"x","state":"OPEN","statusCheckRollup":[{"__typename":%q,"name":"n","context":"n","status":%q,"conclusion":%q,"state":%q}]}`,
			c.typename, c.status, c.conclusion, c.state)
		pr, err := ParsePRView([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if got := pr.Checks[0].Conclusion; got != c.want {
			t.Errorf("%s %s/%s/%s: got %s, want %s", c.typename, c.status, c.conclusion, c.state, got, c.want)
		}
	}
}

func TestParseReviewComments(t *testing.T) {
	got, err := ParseReviewComments(readFixture(t, "review_comments.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("comments %+v", got)
	}
	first, reply := got[0], got[1]
	if first.ID != 5 || first.Line != 17 || first.Author != "alice" || first.Path != "internal/a.go" || first.Body != "Rename this." || first.InReplyTo != 0 {
		t.Fatalf("first %+v", first)
	}
	if !first.CreatedAt.Equal(time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("created %v", first.CreatedAt)
	}
	if reply.ID != 6 || reply.Line != 20 || reply.InReplyTo != 5 || reply.Author != "bob" {
		t.Fatalf("reply %+v", reply)
	}
}

func TestTailLines(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	got := TailLines(b.String(), LogTailLines)
	lines := strings.Split(got, "\n")
	if len(lines) != 150 || lines[0] != "line 51" || lines[149] != "line 200" {
		t.Fatalf("got %d lines, first %q last %q", len(lines), lines[0], lines[len(lines)-1])
	}
	if got := TailLines("a\nb\nc\n", LogTailLines); got != "a\nb\nc" {
		t.Fatalf("short input %q", got)
	}
	if got := TailLines("a\nb\nc\n", 2); got != "b\nc" {
		t.Fatalf("trailing newline counted as a line: %q", got)
	}
}

func TestGHCLIUsesFakeBinary(t *testing.T) {
	bin := t.TempDir()
	fixture, err := filepath.Abs(filepath.Join("testdata", "pr_view.json"))
	if err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ -n "$GH_FAKE_NOPR" ]; then
  echo 'no pull requests found for branch "x"' >&2
  exit 1
fi
if [ "$1" = "pr" ] && [ "$2" = "view" ]; then
  cat '` + fixture + `'
  exit 0
fi
echo "unexpected: $*" >&2
exit 2
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	gh := GHCLI{Timeout: 10 * time.Second}
	pr, err := gh.PRView(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 42 || len(pr.Checks) != 4 {
		t.Fatalf("pr %+v", pr)
	}

	t.Setenv("GH_FAKE_NOPR", "1")
	if _, err := gh.PRView(context.Background(), t.TempDir()); !errors.Is(err, ErrNoPR) {
		t.Fatalf("want ErrNoPR, got %v", err)
	}
}
