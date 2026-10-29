package coordinator

import (
	"reflect"
	"strings"
	"testing"
)

const goodBrief = `## Objective

Parse nested lists in the markdown reader.

## Contract

Parse returns a tree; see docs/specs/PROJ-1.md.

## Owns

internal/parse

## Out of scope

The renderer.

## Acceptance tests

` + "`go test ./internal/parse/...`" + ` covers internal/parse/parse_test.go

## Report must contain

The commands you ran and their exit codes.

## Escalate when

The tree shape has to change.
`

func TestBriefRequiresSevenHeadings(t *testing.T) {
	b, err := ParseBrief(goodBrief)
	if err != nil {
		t.Fatal(err)
	}
	if b.Objective == "" || b.Contract == "" || b.Owns != "internal/parse" || b.OutOfScope != "The renderer." ||
		b.AcceptanceTests == "" || b.Report == "" || b.EscalateWhen != "The tree shape has to change." {
		t.Fatalf("brief = %+v", b)
	}
	md := strings.Replace(goodBrief, "## Acceptance tests", "## Notes", 1)
	_, err = ParseBrief(md)
	if err == nil || !strings.Contains(err.Error(), "## Acceptance tests") && !strings.Contains(err.Error(), "Notes") {
		t.Fatalf("err = %v, want one naming the bad heading", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Fatalf("error %q must be one line", err)
	}
	missing := strings.Replace(goodBrief, "## Escalate when\n\nThe tree shape has to change.\n", "", 1)
	if _, err := ParseBrief(missing); err == nil || !strings.Contains(err.Error(), "## Escalate when") {
		t.Fatalf("err = %v, want one naming ## Escalate when", err)
	}
}

func TestBriefRejectsOldShape(t *testing.T) {
	old := "## Question\n\nDoes the parser accept nested lists?\n\n## Scope\n\ninternal/parse only.\n\n## Report must contain\n\nThe tests you ran.\n"
	_, err := ParseBrief(old)
	if err == nil || !strings.Contains(err.Error(), "## Objective") {
		t.Fatalf("err = %v, want one naming ## Objective", err)
	}
}

func TestBriefRejectsEmptySection(t *testing.T) {
	md := strings.Replace(goodBrief, "## Owns\n\ninternal/parse\n", "## Owns\n", 1)
	if _, err := ParseBrief(md); err == nil || !strings.Contains(err.Error(), "## Owns") {
		t.Fatalf("err = %v, want one naming the empty ## Owns", err)
	}
}

func TestBriefAcceptance(t *testing.T) {
	b := Brief{AcceptanceTests: "`go test ./internal/parse/...` covers internal/parse/parse_test.go"}
	if got := b.AcceptanceCommands(); !reflect.DeepEqual(got, []string{"go test ./internal/parse/..."}) {
		t.Fatalf("commands = %q", got)
	}
	if got := b.AcceptanceFiles(); !reflect.DeepEqual(got, []string{"internal/parse/parse_test.go"}) {
		t.Fatalf("files = %q", got)
	}
}

func TestBriefAcceptanceFileInBackticks(t *testing.T) {
	b := Brief{AcceptanceTests: "- `internal/a/a_test.go`, `make test`"}
	if got := b.AcceptanceFiles(); !reflect.DeepEqual(got, []string{"internal/a/a_test.go"}) {
		t.Fatalf("files = %q", got)
	}
	if got := b.AcceptanceCommands(); !reflect.DeepEqual(got, []string{"make test"}) {
		t.Fatalf("commands = %q", got)
	}
}

func TestBriefMarkdownRoundTrips(t *testing.T) {
	b, err := ParseBrief(goodBrief)
	if err != nil {
		t.Fatal(err)
	}
	md := b.Markdown()
	if !strings.HasPrefix(md, "## Objective\n") {
		t.Fatalf("markdown starts %q", md)
	}
	again, err := ParseBrief(md)
	if err != nil || again != b {
		t.Fatalf("round trip = %+v, %v; want %+v", again, err, b)
	}
}

func TestBriefFromTaskFile(t *testing.T) {
	b, err := ParseBrief(goodBrief)
	if err != nil {
		t.Fatal(err)
	}
	task := "# header\n\n" + BriefStartMarker + "\n" + b.Markdown() + "\n" + ChildGuidelines + "\n" + BriefEndMarker + "\n\n# Task\n\n## PROJ-1: x\n"
	got, ok := BriefFromTaskFile(task)
	if !ok || got != b {
		t.Fatalf("got %+v, %v; want %+v", got, ok, b)
	}
	if _, ok := BriefFromTaskFile("# Task\n\nno brief here\n"); ok {
		t.Fatal("found a brief in a task file with no markers")
	}
	if _, ok := BriefFromTaskFile(BriefStartMarker + "\n## Question\n\nx\n" + BriefEndMarker); ok {
		t.Fatal("accepted an old-shape brief")
	}
}
