package domain

import (
	"io"
	"regexp"
	"strings"
	"time"
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

// TerminalNameFromBranch maps a branch name onto the session's terminal name.
//
// "/" becomes "-", and "." and ":" become "_". The mapping matches the names
// stored on legacy rows, so a terminal name compares equal across old and new
// sessions for the same branch.
func TerminalNameFromBranch(branch string) string {
	s := strings.ReplaceAll(branch, "/", "-")
	s = strings.ReplaceAll(s, ".", "_")
	s = strings.ReplaceAll(s, ":", "_")
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
	seg = strings.ReplaceAll(seg, "_", "-")

	var b strings.Builder
	for _, r := range seg {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '.':
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

const adHocAlphabet = "0123456789abcdefghijklmnopqrstuvwxyz"

const adHocSuffixLen = 4

// AdHocLabel returns prefix + second-resolution timestamp + a 4-char
// [a-z0-9] suffix read from rnd, so two quick sessions created in the same
// second get distinct branch, worktree and terminal names. If rnd fails, the
// suffix is derived from the nanosecond field so it is never omitted.
func AdHocLabel(prefix string, now time.Time, rnd io.Reader) string {
	buf := make([]byte, adHocSuffixLen)
	suffix := make([]byte, adHocSuffixLen)
	if _, err := io.ReadFull(rnd, buf); err == nil {
		for i, b := range buf {
			suffix[i] = adHocAlphabet[int(b)%len(adHocAlphabet)]
		}
	} else {
		n := now.Nanosecond() % (36 * 36 * 36 * 36)
		for i := adHocSuffixLen - 1; i >= 0; i-- {
			suffix[i] = adHocAlphabet[n%36]
			n /= 36
		}
	}
	return prefix + now.Format("20060102-150405") + "-" + string(suffix)
}
