package verify

import "testing"

func TestParseReview(t *testing.T) {
	text := "# Review\n\n## [REQUIRED] Nil map write\ninternal/a.go:42\nwrites to m before make.\n\n" +
		"## [SUGGESTION] Rename x\nb.go:7\nclearer.\n\n" + CompleteMarker + "\n"
	got, complete := ParseReview(text)
	if !complete || len(got) != 2 {
		t.Fatalf("complete=%v findings=%+v", complete, got)
	}
	want := Finding{ID: "f1", Severity: SeverityRequired, Title: "Nil map write", Location: "internal/a.go:42", Body: "internal/a.go:42\nwrites to m before make."}
	if got[0] != want {
		t.Fatalf("got %+v, want %+v", got[0], want)
	}
	if got[1].ID != "f2" || got[1].Severity != SeveritySuggestion || got[1].Location != "b.go:7" {
		t.Fatalf("second = %+v", got[1])
	}
}

func TestParseReviewWithoutMarker(t *testing.T) {
	got, complete := ParseReview("## [REQUIRED] Broken\na.go:1\nbody\n")
	if complete || len(got) != 1 {
		t.Fatalf("complete=%v findings=%+v", complete, got)
	}
}

func TestParseReviewIgnoresUnknownTags(t *testing.T) {
	got, _ := ParseReview("## [NIT] Note\na.go:1\nnot a finding\n\n## [required] Real\nb.go:2\nbody\n")
	if len(got) != 1 || got[0].Severity != SeverityRequired || got[0].Title != "Real" {
		t.Fatalf("findings = %+v", got)
	}
}
