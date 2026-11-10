package aimanskill

import (
	"strings"
	"testing"
)

// reviewModeSection is the skill's ## Review mode section, up to the next
// level-two heading.
func reviewModeSection(t *testing.T) string {
	t.Helper()
	_, rest, ok := strings.Cut(Text, "\n## Review mode\n")
	if !ok {
		t.Fatal("skill has no ## Review mode section")
	}
	// Stop at the next level-two heading outside a code fence: the review
	// file example inside the section has its own ## headings.
	var b strings.Builder
	fenced := false
	for _, line := range strings.Split(rest, "\n") {
		if strings.HasPrefix(line, "```") {
			fenced = !fenced
		}
		if !fenced && strings.HasPrefix(line, "## ") {
			break
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func TestReviewModeRunsAutonomously(t *testing.T) {
	s := reviewModeSection(t)
	for _, gone := range []string{
		"Ask one question at a time",
		"wait for each answer before the next",
		"second publish",
		"The operator then agrees or disagrees with each finding",
	} {
		if strings.Contains(s, gone) {
			t.Errorf("review mode still says %q", gone)
		}
	}
	for _, want := range []string{
		"AskUserQuestion", // the interactive prompt, with answers to pick
		"Final check with operator",
		"approve-review",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("review mode lacks %q", want)
		}
	}
}
