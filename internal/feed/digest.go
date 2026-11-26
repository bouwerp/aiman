package feed

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/bouwerp/aiman/internal/domain"
)

const (
	digestMessages     = 20
	digestMessageBytes = 300
	digestBytes        = 4096
	// DigestKind is the Kind of a folded channel digest event.
	DigestKind = "channel_digest"
)

// cutBytes cuts s to at most n bytes on a rune boundary.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// oneLine folds whitespace so a message stays on one digest line.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// FoldDigests replaces each channel's ambient events with one digest event
// (Kind "channel_digest", ExternalID "digest:<channel>:<first id>-<last id>",
// newest 20 messages, 300 bytes each, 4 KiB in all) and returns the events
// in batch order: urgent and direct first, then digests in channel order of
// first appearance.
func FoldDigests(events []domain.FeedEvent) []domain.FeedEvent {
	var out []domain.FeedEvent
	var order []string
	byChan := map[string][]domain.FeedEvent{}
	for _, e := range events {
		if e.Class != domain.FeedEventClass("ambient") || e.Urgent {
			out = append(out, e)
			continue
		}
		if _, ok := byChan[e.Channel]; !ok {
			order = append(order, e.Channel)
		}
		byChan[e.Channel] = append(byChan[e.Channel], e)
	}
	for _, ch := range order {
		out = append(out, foldChannel(ch, byChan[ch]))
	}
	return out
}

func foldChannel(channel string, evs []domain.FeedEvent) domain.FeedEvent {
	first, last := evs[0], evs[len(evs)-1]
	keep := evs
	if len(keep) > digestMessages {
		keep = keep[len(keep)-digestMessages:]
	}
	// Fill from the newest message back so the 4 KiB cap drops the oldest.
	var lines []string
	size := 0
	for i := len(keep) - 1; i >= 0; i-- {
		e := keep[i]
		line := fmt.Sprintf("[%d] %s: %s", e.ID, e.Actor, cutBytes(oneLine(e.Body), digestMessageBytes))
		if e.URL != "" {
			line += " " + e.URL
		}
		line += "\n"
		if size+len(line) > digestBytes {
			break
		}
		size += len(line)
		lines = append(lines, line)
	}
	slices.Reverse(lines)
	body := strings.Join(lines, "")
	return domain.FeedEvent{
		ID:         last.ID,
		Source:     first.Source,
		ExternalID: fmt.Sprintf("digest:%s:%d-%d", channel, first.ID, last.ID),
		At:         last.At,
		Kind:       DigestKind,
		Title:      fmt.Sprintf("%d messages in %s", len(evs), channel),
		Body:       body,
		ThreadKey:  "slack:" + channel,
		Class:      domain.FeedEventClass("ambient"),
		Channel:    channel,
	}
}
