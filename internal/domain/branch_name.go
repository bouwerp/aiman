package domain

import (
	"regexp"
	"strings"
)

// SanitizeBranchName normalizes a string for use as a git branch name (or
// hierarchical ref like feature/foo). Invalid characters become hyphens;
// empty path segments are dropped; ".lock" suffixes are rewritten.
func SanitizeBranchName(s string) string {
	if s == "" {
		return ""
	}
	s = strings.TrimSpace(s)
	parts := strings.Split(s, "/")
	var out []string
	for _, p := range parts {
		p = sanitizeBranchSegment(p)
		if p == "" {
			continue
		}
		if p == "." || p == ".." {
			p = "part"
		}
		out = append(out, p)
	}
	s = strings.Join(out, "/")
	s = strings.Trim(s, "/")
	if s == "" {
		return ""
	}
	low := strings.ToLower(s)
	if strings.HasSuffix(low, ".lock") {
		s = s[:len(s)-5] + "-lock"
	}
	return s
}

// sanitizeBranchSegment maps a single path component to git-safe characters:
// letters, digits, underscore, hyphen, and dot; other runes become hyphens.
func sanitizeBranchSegment(seg string) string {
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return ""
	}
	seg = strings.ReplaceAll(seg, " ", "-")

	var b strings.Builder
	for _, r := range seg {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '.', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	s := b.String()
	s = regexp.MustCompile(`\.{2,}`).ReplaceAllString(s, ".")
	s = regexp.MustCompile(`-+`).ReplaceAllString(s, "-")
	s = strings.Trim(s, "-.")
	if s == "." || s == ".." {
		return ""
	}
	return s
}
