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

// SanitizeTmuxSessionName maps a branch name onto the name tmux will actually
// store for it.
//
// tmux parses a target as session:window.pane, so a session name containing
// "." or ":" can never be addressed again: `kill-session -t "a.b"` reports
// "can't find pane: b" and leaves the session running. tmux itself rewrites "."
// to "_" when creating the session, so the name aiman remembers must match that
// rewrite or every later capture-pane, send-keys and kill-session silently
// targets nothing.
func SanitizeTmuxSessionName(branch string) string {
	s := strings.ReplaceAll(branch, "/", "-")
	// Match tmux's own normalisation rather than inventing a different one.
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
