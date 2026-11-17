package images

import (
	"fmt"
	"os"
	"strings"
)

// writeReport writes the scan report the steering note points at.
func writeReport(path string, rec Record) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Image vetting: %s\n\n", rec.Ref)
	fmt.Fprintf(&b, "- Digest: `%s` (%s)\n", rec.Digest, rec.Platform)
	if rec.IndexDigest != "" {
		fmt.Fprintf(&b, "- Index: `%s`\n", rec.IndexDigest)
	}
	fmt.Fprintf(&b, "- Config: `%s`\n- Size: %d bytes compressed\n", rec.ConfigDigest, rec.Size)
	fmt.Fprintf(&b, "- Signature: %s\n- Trivy DB: %s\n\n", orNone(rec.Signature), orNone(rec.Scan.TrivyDB))
	section := func(title string, ids []string) {
		fmt.Fprintf(&b, "## %s (%d)\n\n", title, len(ids))
		for _, id := range ids {
			fmt.Fprintf(&b, "- %s\n", id)
		}
		if len(ids) == 0 {
			b.WriteString("none\n")
		}
		b.WriteString("\n")
	}
	section("Critical CVEs", rec.Scan.Critical)
	section("High CVEs", rec.Scan.High)
	section("Static malware hits (ClamAV, YARA)", rec.Scan.StaticHits)
	section("Malicious packages (OSV MAL-)", rec.Scan.MalIDs)
	fmt.Fprintf(&b, "## Image review\n\n- Verdict: %s\n", verdictText(rec.Scan.ModelVerdict))
	if s := rec.Scan; s.Independent != nil {
		f := s.ReviewFindings
		fmt.Fprintf(&b, "- Reviewer: %s %s\n- independent: %v\n- Findings: %d high, %d medium, %d low\n- Report: %s\n",
			s.ReviewAgent, orNone(s.ReviewModel), *s.Independent, f[0], f[1], f[2], orNone(s.ReviewReport))
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// reviewText is the review in the one-line summary.
func reviewText(s Scan) string {
	t := "review " + verdictText(s.ModelVerdict)
	if s.Independent == nil {
		return t
	}
	t += fmt.Sprintf(" (%s; %d high, %d medium findings", s.ReviewAgent, s.ReviewFindings[0], s.ReviewFindings[1])
	if !*s.Independent {
		t += "; not independent: same model family as the session"
	}
	return t + ")"
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func verdictText(v string) string {
	if v == "" {
		return "not run"
	}
	return v
}

// Summary is the scan in one line, for the steering note.
func Summary(rec Record) string {
	static := "static clean"
	if n := len(rec.Scan.StaticHits) + len(rec.Scan.MalIDs); n > 0 {
		static = fmt.Sprintf("%d static hits", n)
	}
	highs := ""
	if len(rec.Scan.High) > 0 {
		highs = " (" + firstIDs(rec.Scan.High, 20) + ")"
	}
	return fmt.Sprintf("%d critical, %d high%s, %s, %s, signature %s",
		len(rec.Scan.Critical), len(rec.Scan.High), highs, static, reviewText(rec.Scan), orNone(rec.Signature))
}
