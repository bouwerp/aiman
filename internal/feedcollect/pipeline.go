package feedcollect

import (
	"strings"
	"unicode/utf8"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/redact"
)

const (
	// MaxBodyBytes bounds an event body after redaction.
	MaxBodyBytes = 4 << 10
	// MaxEnrichmentBytes bounds an event's enrichment after redaction.
	MaxEnrichmentBytes = 8 << 10
	// maxTitleBytes bounds an event title.
	maxTitleBytes = 512
)

// finish is the last pipeline step every event passes before it is stored,
// whichever source made it: the source name is set and every free text field
// is redacted and cut.
func finish(ev domain.FeedEvent, source domain.FeedSource) domain.FeedEvent {
	ev.Source = source
	ev.Title = cut(oneLine(redactText(ev.Title)), maxTitleBytes)
	ev.Body = cut(redactText(ev.Body), MaxBodyBytes)
	ev.Enrichment = cut(redactText(ev.Enrichment), MaxEnrichmentBytes)
	return ev
}

// redactText runs redact.Lines over s line by line.
func redactText(s string) string {
	if s == "" {
		return s
	}
	lines, _ := redact.Lines(strings.Split(s, "\n"), nil)
	return strings.Join(lines, "\n")
}

// cut shortens s to at most n bytes on a rune boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for i := n; i > 0 && n-i < utf8.UTFMax; i-- {
		if utf8.RuneStart(s[i]) {
			return s[:i]
		}
	}
	return s[:n]
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// muted reports whether a live mute covers the event's thread or sender.
// Channel mutes are the Slack source's, which skips those channels itself.
func muted(ev domain.FeedEvent, mutes []domain.FeedMute) bool {
	for _, m := range mutes {
		switch m.Scope {
		case "thread":
			if m.Key == ev.ThreadKey {
				return true
			}
		case "sender":
			if ev.Actor != "" && m.Key == ev.Actor {
				return true
			}
		case "channel":
			if ev.Channel != "" && m.Key == ev.Channel {
				return true
			}
		}
	}
	return false
}

// unmuted drops the events a live mute covers.
func unmuted(evs []domain.FeedEvent, mutes []domain.FeedMute) []domain.FeedEvent {
	if len(mutes) == 0 {
		return evs
	}
	out := evs[:0]
	for _, ev := range evs {
		if !muted(ev, mutes) {
			out = append(out, ev)
		}
	}
	return out
}
