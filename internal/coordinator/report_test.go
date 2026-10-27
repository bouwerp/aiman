package coordinator

import "testing"

func TestReportRequiresEvidence(t *testing.T) {
	cases := []struct {
		name string
		md   string
		ok   bool
	}{
		{"prose only", "## What changed\n\nI fixed the parser and it works now.\n", false},
		{"url is not a repo path", "See https://example.com/a.go for details.\n", false},
		{"absolute path is not repo-relative", "Edited /etc/hosts.conf by hand.\n", false},
		{"path and test command", "## Files\n\n- internal/x.go\n\n## Tests run\n\n`go test ./...` passed\n", true},
		{"path alone", "## Files\n\n`internal/parse/list.go`\n", true},
		{"test command alone", "## Tests run\n\nmake test: ok\n", true},
	}
	for _, c := range cases {
		err := ValidateReport(c.md)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}
