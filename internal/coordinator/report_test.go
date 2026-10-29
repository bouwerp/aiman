package coordinator

import (
	"strings"
	"testing"
)

func TestReportRequiresEvidence(t *testing.T) {
	cases := []struct {
		name string
		md   string
		ok   bool
	}{
		{"prose only", "## Evidence\n\nI fixed the parser and it works now.\n\n## Surprises\n\nnone\n", false},
		{"url is not a repo path", "## Evidence\n\nSee https://example.com/a.go for details.\n\n## Surprises\n\nnone\n", false},
		{"absolute path is not repo-relative", "## Evidence\n\nEdited /etc/hosts.conf by hand.\n\n## Surprises\n\nnone\n", false},
		{"path and test command", "## Evidence\n\n- internal/x.go\n- `go test ./...` exit 0\n\n## Surprises\n\nnone\n", true},
		{"path alone", "## Evidence\n\n`internal/parse/list.go`\n\n## Surprises\n\nnone\n", true},
		{"test command alone", "## Evidence\n\nmake test: exit 0\n\n## Surprises\n\nnone\n", true},
	}
	for _, c := range cases {
		err := ValidateReport(c.md)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestReportNeedsEvidenceAndSurprises(t *testing.T) {
	good := "## Evidence\n\n`go test ./...` exit 0\n\n## Surprises\n\nnone\n"
	if err := ValidateReport(good); err != nil {
		t.Fatalf("good report: %v", err)
	}
	noSurprises := "## Evidence\n\n`go test ./...` exit 0\n"
	if err := ValidateReport(noSurprises); err == nil || !strings.Contains(err.Error(), "## Surprises") {
		t.Fatalf("err = %v, want one naming ## Surprises", err)
	}
	noEvidence := "## Surprises\n\nnone\n\n## Tests run\n\ngo test ./...\n"
	if err := ValidateReport(noEvidence); err == nil || !strings.Contains(err.Error(), "## Evidence") {
		t.Fatalf("err = %v, want one naming ## Evidence", err)
	}
	// A citation outside ## Evidence does not count.
	outside := "## Files\n\ninternal/a/b.go\n\n## Evidence\n\nit works\n\n## Surprises\n\nnone\n"
	if err := ValidateReport(outside); err == nil {
		t.Fatal("accepted evidence cited outside ## Evidence")
	}
}

func TestReportSurprises(t *testing.T) {
	for _, body := range []string{"None", "none.", "- None", "N/A", "n/a."} {
		md := "## Evidence\n\nmake ci exit 0\n\n## Surprises\n\n" + body + "\n"
		if text, any := ReportSurprises(md); any {
			t.Errorf("%q: any = true, text %q", body, text)
		}
	}
	md := "## Evidence\n\nmake ci exit 0\n\n## Surprises\n\nThe Store interface gained a Close method.\n\n## Notes\n\nx\n"
	text, any := ReportSurprises(md)
	if !any || text != "The Store interface gained a Close method." {
		t.Fatalf("text = %q, any = %v", text, any)
	}
}
