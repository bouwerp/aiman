package notify

import (
	"strings"
	"unicode/utf8"

	"github.com/bouwerp/aiman/internal/domain"
)

// Label returns the display label for a session.
func Label(issueKey, name, id string) string {
	if issueKey != "" {
		return issueKey
	}
	if name != "" {
		return name
	}
	if len(id) >= 8 {
		return id[:7]
	}
	return id
}

// Format builds the notification text.
func Format(label, host string, kind domain.NotifyKind, detail string) string {
	d := detail
	if d != "" {
		d = strings.ReplaceAll(d, "\n", " ")
		d = strings.Join(strings.Fields(d), " ")
		d = strings.TrimSpace(d)
		if d == "" {
			detail = ""
		} else {
			if utf8.RuneCountInString(d) > 200 {
				r := []rune(d)
				d = string(r[:200]) + "…"
			}
			detail = d
		}
	}
	base := label + " on " + host + ": " + domain.EventLabel(kind)
	if detail == "" {
		return base
	}
	return base + " · " + detail
}
