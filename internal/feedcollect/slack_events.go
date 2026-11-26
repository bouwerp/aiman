package feedcollect

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/slack"
)

// slackDropSubtypes are housekeeping messages the pre-filter drops.
var slackDropSubtypes = map[string]bool{
	"channel_join": true, "channel_leave": true, "group_join": true, "group_leave": true,
	"channel_topic": true, "group_topic": true, "channel_purpose": true, "group_purpose": true,
	"channel_name": true, "group_name": true, "channel_archive": true, "channel_unarchive": true,
	"group_archive": true, "group_unarchive": true, "pinned_item": true, "unpinned_item": true,
	"message_deleted": true, "message_changed": true, "channel_convert_to_private": true,
	"channel_convert_to_public": true, "bot_add": true, "bot_remove": true,
}

// slackThreadContextMax bounds the thread context kept as enrichment; finish
// cuts it again at MaxEnrichmentBytes after redaction.
const slackThreadContextMax = 20

// messageEvent turns a message into an event, or reports false when the
// pre-filter drops it. threadTS is the parent of a reply, empty for a
// top-level message; inThread says the member is in that thread.
func (s *slackSource) messageEvent(c *slackConv, m slack.Message, threadTS string, inThread bool, enrichment string) (domain.FeedEvent, bool) {
	if m.TS == "" || slackDropSubtypes[m.Subtype] {
		return domain.FeedEvent{}, false
	}
	actor := m.User
	if m.BotID != "" || m.Subtype == "bot_message" {
		if !s.bots[m.BotID] {
			return domain.FeedEvent{}, false
		}
		actor = m.BotID
	}
	own := m.User != "" && m.User == s.o.Self
	if own && threadTS == "" {
		return domain.FeedEvent{}, false
	}
	direct := c.dm || mentions(m.Text, s.o.Self) || (threadTS != "" && inThread)
	ev := domain.FeedEvent{
		ExternalID: c.id + "/" + m.TS,
		At:         slackTime(m.TS),
		Actor:      actor,
		Kind:       "message",
		Title:      convTitle(c.id, c.name, c.dm),
		Body:       messageBody(m),
		URL:        archiveURL(c.id, m.TS, threadTS),
		ThreadKey:  domain.FeedThreadKeySlack(c.id, threadTS, m.TS),
		Class:      "ambient",
		Channel:    c.id,
		Enrichment: enrichment,
	}
	switch {
	case own:
		ev.Own = true
		ev.Class = "direct"
	case direct:
		ev.Class = "direct"
		ev.Urgent = true
	}
	return ev, true
}

// searchEvent turns a mention search hit into a direct event. c is nil for a
// conversation the listing has not returned.
func (s *slackSource) searchEvent(c *slackConv, h slack.SearchHit) domain.FeedEvent {
	threadTS := ""
	if l, err := slack.ParseArchiveLink(h.Permalink); err == nil && l.ThreadTS != "" && l.ThreadTS != h.TS {
		threadTS = l.ThreadTS
	}
	name, dm := h.Name, false
	if c != nil {
		name, dm = c.name, c.dm
	}
	link := h.Permalink
	if link == "" {
		link = archiveURL(h.Channel, h.TS, threadTS)
	}
	return domain.FeedEvent{
		ExternalID: h.Channel + "/" + h.TS,
		At:         slackTime(h.TS),
		Actor:      h.User,
		Kind:       "message",
		Title:      convTitle(h.Channel, name, dm),
		Body:       h.Text,
		URL:        link,
		ThreadKey:  domain.FeedThreadKeySlack(h.Channel, threadTS, h.TS),
		Class:      "direct",
		Urgent:     true,
		Channel:    h.Channel,
	}
}

func mentions(text, self string) bool {
	if self == "" {
		return false
	}
	return strings.Contains(text, "<@"+self+">") || strings.Contains(text, "<@"+self+"|")
}

func convTitle(id, name string, dm bool) string {
	switch {
	case dm && strings.HasPrefix(name, "mpdm-"):
		return "Slack group DM"
	case dm:
		return "Slack DM"
	case name != "":
		return "#" + name
	default:
		return "#" + id
	}
}

// messageBody is the text plus one line per shared file. Files are named,
// never downloaded.
func messageBody(m slack.Message) string {
	var b strings.Builder
	b.WriteString(m.Text)
	for _, f := range m.Files {
		name := f.Title
		if name == "" {
			name = f.Name
		}
		if name == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("[file: " + name + "]")
	}
	return b.String()
}

// threadContext renders the parent and the replies before a reply, newest
// last, as the reply's enrichment.
func threadContext(before []slack.Message) string {
	if len(before) == 0 {
		return ""
	}
	if len(before) > slackThreadContextMax {
		before = append(before[:1:1], before[len(before)-slackThreadContextMax+1:]...)
	}
	var b strings.Builder
	b.WriteString("Thread so far:\n")
	for _, m := range before {
		who := m.User
		if who == "" {
			who = m.BotID
		}
		fmt.Fprintf(&b, "- %s: %s\n", who, oneLine(m.Text))
	}
	return strings.TrimRight(b.String(), "\n")
}

// archiveURL is the message's archive link; a reply carries its thread.
func archiveURL(channel, ts, threadTS string) string {
	digits := strings.Replace(ts, ".", "", 1)
	u := "https://slack.com/archives/" + channel + "/p" + digits
	if threadTS != "" {
		u += "?thread_ts=" + threadTS + "&cid=" + channel
	}
	return u
}

// slackTSOf is t as a Slack timestamp.
func slackTSOf(t time.Time) string {
	return fmt.Sprintf("%d.%06d", t.Unix(), t.Nanosecond()/1000)
}

// slackTime reads a Slack timestamp; a bad one is the zero time.
func slackTime(ts string) time.Time {
	sec, frac, _ := strings.Cut(ts, ".")
	s, err := strconv.ParseInt(sec, 10, 64)
	if err != nil || ts == "" {
		return time.Time{}
	}
	var us int64
	if frac != "" {
		f := (frac + "000000")[:6]
		if us, err = strconv.ParseInt(f, 10, 64); err != nil {
			us = 0
		}
	}
	return time.Unix(s, us*1000).UTC()
}

// tsCompare orders two Slack timestamps numerically; empty is the oldest.
func tsCompare(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return -1
	case b == "":
		return 1
	}
	as, af, _ := strings.Cut(a, ".")
	bs, bf, _ := strings.Cut(b, ".")
	if len(as) != len(bs) {
		if len(as) < len(bs) {
			return -1
		}
		return 1
	}
	if c := strings.Compare(as, bs); c != 0 {
		return c
	}
	af, bf = (af + "000000")[:6], (bf + "000000")[:6]
	return strings.Compare(af, bf)
}

func tsAfter(a, b string) bool { return tsCompare(a, b) > 0 }

// convCursor is a conversation's cursor: the ts read up to, and while a read
// cut short is closing its gap, the oldest message it got (bound) and the
// newest (newest).
type convCursor struct{ ts, bound, newest string }

func parseConvCursor(v, start string) convCursor {
	f := strings.Fields(v)
	switch len(f) {
	case 0:
		return convCursor{ts: start}
	case 3:
		return convCursor{ts: f[0], bound: f[1], newest: f[2]}
	default:
		return convCursor{ts: f[0]}
	}
}

func (c convCursor) String() string {
	if c.bound == "" {
		return c.ts
	}
	return c.ts + " " + c.bound + " " + c.newest
}

// threadCursor is a thread's cursor: the reply count a history answer
// showed, the count when it was last read, the newest reply read, whether
// the member is in it and the newest latest_reply a history answer showed.
// The stored form is "seen read last mine [latest]".
type threadCursor struct {
	parent     string
	seen, read int
	last       string
	mine       bool
	latest     string
}

func parseThreadCursor(v, parent string) threadCursor {
	t := threadCursor{parent: parent, last: parent}
	f := strings.Fields(v)
	if len(f) != 4 && len(f) != 5 {
		return t
	}
	t.seen, _ = strconv.Atoi(f[0])
	t.read, _ = strconv.Atoi(f[1])
	t.last = f[2]
	t.mine = f[3] == "1"
	if len(f) == 5 {
		t.latest = f[4]
	}
	return t
}

func (t threadCursor) String() string {
	mine := "0"
	if t.mine {
		mine = "1"
	}
	out := fmt.Sprintf("%d %d %s %s", t.seen, t.read, t.last, mine)
	if t.latest != "" {
		out += " " + t.latest
	}
	return out
}

// activity is the thread's newest known message: parent, reply read or
// latest_reply shown.
func (t threadCursor) activity() string {
	a := t.parent
	for _, ts := range []string{t.last, t.latest} {
		if tsAfter(ts, a) {
			a = ts
		}
	}
	return a
}
