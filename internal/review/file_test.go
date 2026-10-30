package review

import (
	"slices"
	"strings"
	"testing"
)

const sample = `# Review: owner/repo#123

## Questions

### Does the retry cover the batch path too?
> yes, through the shared client

## Findings

### F1 blocking internal/x/y.go:42
Verified: yes (go test ./internal/x/ -run TestY fails)
Source: primary
Call: agree "real bug"

Nil deref when the list is empty.

### F2 nit internal/x/z.go:7
Verified: no
Source: secondary
Call: disagree "style only"

Rename this.

## Summary

Looks mostly fine.
`

func TestParseFileFormat(t *testing.T) {
	f, err := ParseFile(sample)
	if err != nil {
		t.Fatal(err)
	}
	want := []Question{{Text: "Does the retry cover the batch path too?", Answer: "yes, through the shared client"}}
	if !slices.Equal(f.Questions, want) {
		t.Fatalf("questions: %+v", f.Questions)
	}
	if len(f.Findings) != 2 {
		t.Fatalf("findings: %+v", f.Findings)
	}
	a := f.Findings[0]
	if a.ID != "F1" || a.Label != "blocking" || a.Path != "internal/x/y.go" || a.Line != 42 || !a.Verified ||
		a.Evidence != "go test ./internal/x/ -run TestY fails" || a.Source != "primary" ||
		a.Call != "agree" || a.Quote != "real bug" || a.Body != "Nil deref when the list is empty." {
		t.Fatalf("F1: %+v", a)
	}
	b := f.Findings[1]
	if b.Verified || b.Call != "disagree" || b.Source != "secondary" {
		t.Fatalf("F2: %+v", b)
	}
	if f.Summary != "Looks mostly fine." {
		t.Fatalf("summary: %q", f.Summary)
	}
	if m := f.Missing(); len(m) != 1 || !strings.Contains(m[0], "F2") {
		t.Fatalf("missing: %v", m)
	}
}

func TestMissing(t *testing.T) {
	md := `# Review: o/r#1

## Questions

### Is the old endpoint still called?
> no

### Should the cache be per tenant?

## Findings

### F1 blocking a.go:1
Verified: no
Source: primary

body

### F2 suggestion b.go:2
Verified: yes (ran it)
Source: both

body
`
	f, err := ParseFile(md)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(f.Missing(), "\n")
	for _, want := range []string{`question "Should the cache be per tenant?" has no answer`, "F1: no call", "F2: no call", "F1: unverified blocking"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing list lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "F2: unverified") {
		t.Errorf("verified finding flagged:\n%s", got)
	}
}

func TestMissingAllowsUnverifiedQuestion(t *testing.T) {
	f, err := ParseFile(strings.Replace(sample, "### F2 nit", "### F2 question", 1))
	if err != nil {
		t.Fatal(err)
	}
	if m := f.Missing(); len(m) != 0 {
		t.Fatalf("missing: %v", m)
	}
}

func TestPostFiltersAndLabels(t *testing.T) {
	f, err := ParseFile(strings.Replace(sample, "### F2 nit", "### F2 question", 1))
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.Post("REQUEST_CHANGES", "abc123", false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Event != "REQUEST_CHANGES" || p.CommitID != "abc123" || p.Body != "Looks mostly fine." {
		t.Fatalf("post: %+v", p)
	}
	if len(p.Comments) != 1 || p.Comments[0].Path != "internal/x/y.go" || p.Comments[0].Line != 42 ||
		p.Comments[0].Body != "blocking: Nil deref when the list is empty." {
		t.Fatalf("comments: %+v", p.Comments)
	}
	if _, err := f.Post("APPROVE", "abc123", false); err == nil {
		t.Fatal("APPROVE accepted")
	}
	s, err := f.Post("COMMENT", "abc123", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.Body, "Reviewed by one agent; the second reviewer was unavailable.") {
		t.Fatalf("body: %q", s.Body)
	}
}

func TestPostRefusesIncomplete(t *testing.T) {
	f, err := ParseFile(sample) // F2 is an unverified nit
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Post("COMMENT", "abc", false); err == nil {
		t.Fatal("incomplete review posted")
	}
}

func missingFor(t *testing.T, md string) string {
	t.Helper()
	f, err := ParseFile(md)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(f.Missing(), "\n")
}

const answersOnly = "## Findings\n\n"

func TestMissingAgreedNeedsLocation(t *testing.T) {
	agreed := answersOnly + "### F1 nit\nVerified: yes (ran)\nCall: agree \"ok\"\n\nbody\n\n## Summary\n\ns\n"
	if got := missingFor(t, agreed); !strings.Contains(got, "F1: agreed finding needs path:line") {
		t.Fatalf("got %q", got)
	}
	zero := strings.Replace(agreed, "F1 nit", "F1 nit a.go:0", 1)
	if got := missingFor(t, zero); !strings.Contains(got, "F1: agreed finding needs path:line") {
		t.Fatalf("line 0: %q", got)
	}
	disagreed := strings.Replace(agreed, "Call: agree", "Call: disagree", 1)
	if got := missingFor(t, disagreed); got != "" {
		t.Fatalf("disagreed flagged: %q", got)
	}
}

func TestMissingCallNeedsQuote(t *testing.T) {
	for _, call := range []string{"agree", "disagree"} {
		md := answersOnly + "### F1 nit a.go:1\nVerified: yes (ran)\nCall: " + call + "\n\nbody\n\n## Summary\n\ns\n"
		if got := missingFor(t, md); !strings.Contains(got, "F1: call needs the operator's quote") {
			t.Errorf("%s: %q", call, got)
		}
	}
}

func TestMissingEvidenceBodySummary(t *testing.T) {
	noEvidence := answersOnly + "### F1 nit a.go:1\nVerified: yes\nCall: agree \"ok\"\n\nbody\n\n## Summary\n\ns\n"
	if got := missingFor(t, noEvidence); !strings.Contains(got, "F1: verified finding needs evidence") {
		t.Errorf("evidence: %q", got)
	}
	noBody := answersOnly + "### F1 nit a.go:1\nVerified: yes (ran)\nCall: agree \"ok\"\n\n## Summary\n\ns\n"
	if got := missingFor(t, noBody); !strings.Contains(got, "F1: agreed finding needs a comment body") {
		t.Errorf("body: %q", got)
	}
	noSummary := answersOnly + "## Summary\n\n"
	if got := missingFor(t, noSummary); got != "summary is empty" {
		t.Errorf("summary: %q", got)
	}
}

// The agent asks only what it could not settle, so a file with no questions
// is complete.
func TestNoQuestionsIsComplete(t *testing.T) {
	md := "# Review: o/r#1\n\n## Findings\n\n### F1 nit a.go:1\nVerified: yes (ran)\nSource: primary\nCall: agree \"ok\"\n\nbody\n\n## Summary\n\ns\n"
	if got := missingFor(t, md); got != "" {
		t.Fatalf("missing: %q", got)
	}
}
