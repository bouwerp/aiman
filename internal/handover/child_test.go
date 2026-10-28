package handover

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func testBriefInput() BriefInput {
	return BriefInput{
		Dir:        "/home/u/.aiman/handovers/run-1",
		Transcript: "/home/u/.claude/projects/x/abc.jsonl",
		Worktree:   "/home/u/src/repo@wtb-12",
		Reason:     "restart",
	}
}

func TestChildBriefPaths(t *testing.T) {
	out := ChildBrief(testBriefInput())
	for _, want := range []string{
		"/home/u/.aiman/handovers/run-1/brief.md",
		"/home/u/.aiman/handovers/run-1/mechanical.md",
		"/home/u/.aiman/handovers/run-1/handover.md",
		"/home/u/.aiman/handovers/run-1/memories.json",
		"/home/u/.aiman/handovers/run-1/done",
		"/home/u/.claude/projects/x/abc.jsonl",
		"/home/u/src/repo@wtb-12",
		"Reason: restart",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("brief missing %q", want)
		}
	}
	if strings.Contains(out, "—") {
		t.Error("brief contains an em-dash")
	}
}

func TestChildBriefHeadingsAndRules(t *testing.T) {
	out := ChildBrief(testBriefInput())
	for _, h := range RequiredSections {
		if !strings.Contains(out, h) {
			t.Errorf("brief missing heading %q", h)
		}
	}
	lower := strings.ToLower(out)
	for _, want := range []string{
		"read-only", "may be gone",
		"do not edit", "do not commit", "do not push",
		"to-do", "steering", "child session",
		"decision", "pitfall", "convention", "fact",
		`"candidates"`, `"title"`, `"abstract"`, `"body"`,
		"at most 5",
		"write the done file last",
	} {
		if !strings.Contains(lower, strings.ToLower(want)) {
			t.Errorf("brief missing %q", want)
		}
	}
}

func TestChildBriefNoTranscript(t *testing.T) {
	in := testBriefInput()
	in.Transcript = ""
	if out := ChildBrief(in); !strings.Contains(out, "No transcript") {
		t.Errorf("brief should say there is no transcript:\n%s", out)
	}
}

func validChildHandover() string {
	var b strings.Builder
	b.WriteString("# Handover: WTB-12\n\n")
	for _, h := range RequiredSections {
		b.WriteString(h + "\n\nText.\n\n")
	}
	return b.String()
}

func TestParseChildHandoverValid(t *testing.T) {
	md := validChildHandover()
	got, err := ParseChildHandover([]byte(md))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != md {
		t.Errorf("got %q, want input unchanged", got)
	}
}

func TestParseChildHandoverMissingSection(t *testing.T) {
	md := strings.Replace(validChildHandover(), "## Evidence\n", "", 1)
	_, err := ParseChildHandover([]byte(md))
	if err == nil || !strings.Contains(err.Error(), "## Evidence") {
		t.Fatalf("err = %v, want missing ## Evidence", err)
	}
}

func TestParseChildHandoverTooLarge(t *testing.T) {
	md := validChildHandover() + strings.Repeat("x", MaxChildHandoverBytes)
	if _, err := ParseChildHandover([]byte(md)); err == nil {
		t.Fatal("want error for oversize handover")
	}
}

func TestParseChildHandoverEmpty(t *testing.T) {
	if _, err := ParseChildHandover([]byte("  \n")); err == nil {
		t.Fatal("want error for empty handover")
	}
}

func TestParseChildCandidates(t *testing.T) {
	raw := `{"candidates":[
		{"type":"decision","title":"d1","abstract":"a","body":"b"},
		{"type":"habit","title":"dropped"},
		{"type":"pitfall","title":""},
		{"type":"fact","title":"f1"},
		{"type":"convention","title":"c1"},
		{"type":"fact","title":"f2"},
		{"type":"pitfall","title":"p1"},
		{"type":"fact","title":"over cap"}
	]}`
	got := ParseChildCandidates([]byte(raw))
	if len(got) != domain.MaxMemoryCandidates {
		t.Fatalf("len = %d, want %d: %+v", len(got), domain.MaxMemoryCandidates, got)
	}
	if got[0] != (domain.MemoryCandidate{Type: domain.ContextTypeDecision, Title: "d1", Abstract: "a", Body: "b"}) {
		t.Errorf("first = %+v", got[0])
	}
	if got[4].Title != "p1" {
		t.Errorf("last = %+v, want p1", got[4])
	}
}

func TestParseChildCandidatesBadJSON(t *testing.T) {
	for _, raw := range []string{"", "not json", `{"candidates":`, `[1,2]`} {
		if got := ParseChildCandidates([]byte(raw)); got != nil {
			t.Errorf("%q: got %+v, want nil", raw, got)
		}
	}
}
