// Package redact scrubs secrets from log lines before they leave the host. It
// is a screen filter, not a permission boundary: a pattern it does not know
// passes through.
package redact

import (
	"regexp"
	"strings"
)

// Mark replaces each secret.
const Mark = "[redacted]"

// rule replaces every match of re with repl, which may keep a prefix group.
type rule struct {
	re   *regexp.Regexp
	repl string
}

// rules run in order; earlier, more specific ones win, and a replaced span
// never matches a later rule.
var rules = []rule{
	// PEM armour lines; Lines also hides the body between them.
	{regexp.MustCompile(`-----(?:BEGIN|END)[A-Z0-9 ]*-----`), Mark},
	{regexp.MustCompile(`(hooks\.slack\.com/services/)[A-Za-z0-9/_-]+`), "${1}" + Mark},
	{regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]+`), Mark},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{16,}`), Mark},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{16,}`), Mark},
	{regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{8,}`), Mark},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`), Mark},
	{regexp.MustCompile(`\bxai-[A-Za-z0-9_-]{16,}`), Mark},
	{regexp.MustCompile(`\bExp(?:onent|o)PushToken\[[^\]\s]*\]?`), Mark},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`), Mark},
	{regexp.MustCompile(`(?i)(aws_secret_access_key["']?\s*[=:]\s*["']?)[A-Za-z0-9/+=]{40}`), "${1}" + Mark},
	{regexp.MustCompile(`(?i)\b(Bearer\s+)[A-Za-z0-9._~+/=-]+`), "${1}" + Mark},
	{regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^/\s:@]+:[^/\s@]+@`), "${1}" + Mark + "@"},
	{regexp.MustCompile(`(?i)\b((?:password|passwd|secret|access_token|token|code|key)=)[^\s&"',;]+`), "${1}" + Mark},
}

// Line returns s with every known secret, and every non-empty exact value,
// replaced by Mark, and the number of replacements.
func Line(s string, exact []string) (string, int) {
	n := 0
	for _, v := range exact {
		if v == "" {
			continue
		}
		if c := strings.Count(s, v); c > 0 {
			n += c
			s = strings.ReplaceAll(s, v, Mark)
		}
	}
	for _, r := range rules {
		if hits := len(r.re.FindAllStringIndex(s, -1)); hits > 0 {
			n += hits
			s = r.re.ReplaceAllString(s, r.repl)
		}
	}
	return s, n
}

var pemBegin = regexp.MustCompile(`-----BEGIN[A-Z0-9 ]*-----`)
var pemEnd = regexp.MustCompile(`-----END[A-Z0-9 ]*-----`)

// Lines runs Line over each line and also replaces every line inside a PEM
// block (between its BEGIN and END lines) with Mark, since a body line alone
// looks like any base64. It returns new lines and the total count.
func Lines(lines []string, exact []string) ([]string, int) {
	out := make([]string, len(lines))
	total, inPEM := 0, false
	for i, l := range lines {
		begin, end := pemBegin.MatchString(l), pemEnd.MatchString(l)
		if inPEM && !begin && !end {
			out[i] = Mark
			total++
			continue
		}
		switch {
		case begin && !end:
			inPEM = true
		case end:
			inPEM = false
		}
		var n int
		out[i], n = Line(l, exact)
		total += n
	}
	return out, total
}
