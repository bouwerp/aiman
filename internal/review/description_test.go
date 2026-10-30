package review

import (
	"slices"
	"strings"
	"testing"
)

func TestLinkedIssues(t *testing.T) {
	body := "Closes #12 and fixes PROJ-1; see other/repo#3, #12 again, and abc#x. Issue-1 is not a key."
	got := LinkedIssues(body)
	want := []string{"#12", "PROJ-1", "other/repo#3"}
	if !slices.Equal(got, want) {
		t.Fatalf("linked %q, want %q", got, want)
	}
	if got := LinkedIssues("no links here"); len(got) != 0 {
		t.Fatalf("linked %q, want none", got)
	}
}

func TestDescription(t *testing.T) {
	md := Description(PR{Title: "Add the parser", BaseRef: "main", Body: "Parses lists.\n\nCloses #12."})
	want := "# Add the parser\n\nBase: main\nLinked issues: #12\n\nParses lists.\n\nCloses #12.\n"
	if md != want {
		t.Fatalf("description:\n%s\nwant:\n%s", md, want)
	}
	if md := Description(PR{Title: "T", BaseRef: "main"}); !strings.Contains(md, "Linked issues: none") || !strings.Contains(md, "(no description)") {
		t.Fatalf("empty body description:\n%s", md)
	}
}
