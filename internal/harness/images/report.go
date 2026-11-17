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
	fmt.Fprintf(&b, "## Image review\n\n%s\n", verdictText(rec.Scan.ModelVerdict))
	return os.WriteFile(path, []byte(b.String()), 0o600)
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
	return fmt.Sprintf("%d critical, %d high%s, %s, review %s, signature %s",
		len(rec.Scan.Critical), len(rec.Scan.High), highs, static, verdictText(rec.Scan.ModelVerdict), orNone(rec.Signature))
}
