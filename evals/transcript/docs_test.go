package transcripteval

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func readRepoDoc(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// transcriptParagraphs joins the AGENTS.md lines (one paragraph each) that
// name session.transcript.
func transcriptParagraphs(agents string) string {
	var keep []string
	for _, line := range strings.Split(agents, "\n") {
		if strings.Contains(line, "session.transcript") {
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n")
}

// TestEvalTranscriptDocs checks P6's docs: AGENTS.md documents the three
// methods, the transcript event and the pane fallback; api/openapi.yaml lists
// the methods and the event type; the spec records its deviations.
func TestEvalTranscriptDocs(t *testing.T) {
	agents := readRepoDoc(t, "AGENTS.md")
	for _, re := range []string{
		"`session\\.transcript[` ]",
		"`session\\.transcript_item[` ]",
		"`session\\.process_output[` ]",
	} {
		if !regexp.MustCompile(re).MatchString(agents) {
			t.Errorf("AGENTS.md does not name %s", re)
		}
	}
	para := transcriptParagraphs(agents)
	if !regexp.MustCompile("`transcript[` ][^.\\n]*event|event[^.\\n]*`transcript[` ]").MatchString(para) {
		t.Error("AGENTS.md does not describe the transcript event beside session.transcript")
	}
	if !regexp.MustCompile(`(?i)\bpane\b[^.\n]*(fallback|falls back)|(fallback|falls back)[^.\n]*\bpane\b`).MatchString(para) {
		t.Error("AGENTS.md does not describe the pane fallback beside session.transcript")
	}

	openapi := readRepoDoc(t, "api/openapi.yaml")
	for _, m := range []string{"session.transcript", "session.transcript_item", "session.process_output"} {
		if !regexp.MustCompile(`(?m)^\s*- ` + regexp.QuoteMeta(m) + `\s*$`).MatchString(openapi) {
			t.Errorf("api/openapi.yaml does not list method %s", m)
		}
	}
	if !regexp.MustCompile(`(?m)^\s*- transcript\s*$`).MatchString(openapi) {
		t.Error("api/openapi.yaml does not list the transcript event type")
	}

	spec := readRepoDoc(t, "docs/specs/phone-transcript.md")
	if !regexp.MustCompile(`(?m)^#+ Deviations from the approved design\s*$`).MatchString(spec) {
		t.Error(`docs/specs/phone-transcript.md has no "Deviations from the approved design" heading`)
	}
}
