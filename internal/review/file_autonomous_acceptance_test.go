package review

import (
	"strings"
	"testing"
)

// Autonomous review mode: the review agent makes each finding's call itself,
// so a Call line needs no operator quote. Questions still need answers.

func TestCallWithoutQuoteIsComplete(t *testing.T) {
	for _, call := range []string{"agree", "disagree"} {
		md := "## Findings\n\n### F1 nit a.go:1\nVerified: yes (ran)\nSource: primary\nCall: " + call + "\n\nbody\n\n## Summary\n\ns\n"
		f, err := ParseFile(md)
		if err != nil {
			t.Fatalf("%s: %v", call, err)
		}
		if m := f.Missing(); len(m) != 0 {
			t.Errorf("Call: %s with no quote reported missing: %q", call, m)
		}
		if f.Findings[0].Call != call {
			t.Errorf("Call = %q, want %q", f.Findings[0].Call, call)
		}
	}
}

func TestCallQuoteStillParsed(t *testing.T) {
	md := "## Findings\n\n### F1 nit a.go:1\nVerified: yes (ran)\nCall: disagree \"style only\"\n\nbody\n\n## Summary\n\ns\n"
	f, err := ParseFile(md)
	if err != nil {
		t.Fatal(err)
	}
	if f.Findings[0].Quote != "style only" {
		t.Errorf("Quote = %q", f.Findings[0].Quote)
	}
}

func TestFindingStillNeedsACall(t *testing.T) {
	md := "## Findings\n\n### F1 nit a.go:1\nVerified: yes (ran)\n\nbody\n\n## Summary\n\ns\n"
	f, err := ParseFile(md)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.Missing(), "; "); !strings.Contains(got, "F1: no call") {
		t.Errorf("missing = %q, want a missing call", got)
	}
}

func TestUnansweredQuestionStillIncomplete(t *testing.T) {
	md := "## Questions\n\n### Is the batch path covered?\n\n## Findings\n\n### F1 nit a.go:1\nVerified: yes (ran)\nCall: agree\n\nbody\n\n## Summary\n\ns\n"
	f, err := ParseFile(md)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Missing()) == 0 {
		t.Error("an unanswered question must keep the file incomplete")
	}
}
