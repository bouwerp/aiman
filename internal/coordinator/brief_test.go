package coordinator

import (
	"strings"
	"testing"
)

const goodBrief = `## Question

Does the parser accept nested lists?

## Scope

internal/parse only.

## Report must contain

The tests you ran.
`

func TestBriefRequiresThreeHeadings(t *testing.T) {
	md := strings.Replace(goodBrief, "## Scope", "## Notes", 1)
	_, err := ParseBrief(md)
	if err == nil || !strings.Contains(err.Error(), "## Scope") {
		t.Fatalf("err = %v, want one naming ## Scope", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Fatalf("error %q must be one line", err)
	}
}

func TestBriefRejectsEmptySection(t *testing.T) {
	md := "## Question\n\nDoes the parser accept nested lists here?\n\n## Scope\n\n## Report must contain\n\nThe tests you ran.\n"
	if _, err := ParseBrief(md); err == nil || !strings.Contains(err.Error(), "## Scope") {
		t.Fatalf("err = %v, want one naming the empty ## Scope", err)
	}
}

func TestBriefWordFloor(t *testing.T) {
	five := "## Question\n\nWhy?\n\n## Scope\n\nParser only.\n\n## Report must contain\n\nTests run.\n"
	if _, err := ParseBrief(five); err == nil || !strings.Contains(err.Error(), "8") {
		t.Fatalf("five words: err = %v, want the 8-word floor", err)
	}
	eight := "## Question\n\nWhy nested lists?\n\n## Scope\n\nParser only.\n\n## Report must contain\n\nTests run, results.\n"
	b, err := ParseBrief(eight)
	if err != nil {
		t.Fatalf("eight words: %v", err)
	}
	if b.Question != "Why nested lists?" || b.Scope != "Parser only." || b.Report != "Tests run, results." {
		t.Fatalf("brief = %+v", b)
	}
}

func TestBriefMarkdownRoundTrips(t *testing.T) {
	b, err := ParseBrief(goodBrief)
	if err != nil {
		t.Fatal(err)
	}
	md := b.Markdown()
	if !strings.HasPrefix(md, "## Question\n") {
		t.Fatalf("markdown starts %q", md)
	}
	again, err := ParseBrief(md)
	if err != nil || again != b {
		t.Fatalf("round trip = %+v, %v; want %+v", again, err, b)
	}
}
