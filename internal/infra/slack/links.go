package slack

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// ArchiveLink is a message link Slack's "Copy link" gives:
// https://<workspace>.slack.com/archives/<C…>/p<16 digits>[?thread_ts=<ts>&cid=<C…>].
// TS is the linked message; ThreadTS is the parent named by thread_ts, set
// only when the link is to a reply. A channel-only link has no TS.
type ArchiveLink struct {
	Channel  string
	TS       string
	ThreadTS string
}

// ErrNotArchiveLink is returned for a URL that is not a Slack archive link.
var ErrNotArchiveLink = errors.New("not a Slack archive link")

var (
	archivePathRe = regexp.MustCompile(`^/archives/([A-Z0-9]+)(?:/p(\d{10})(\d{6}))?/?$`)
	slackTSRe     = regexp.MustCompile(`^\d{10}\.\d{6}$`)
)

// LooksLikeURL reports whether s is meant as a URL rather than an id or name.
func LooksLikeURL(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.Contains(s, "://") || strings.HasPrefix(s, "www.") || strings.Contains(s, ".slack.com/")
}

// ParseArchiveLink reads a Slack archive link. Anything else is
// ErrNotArchiveLink, wrapped with the reason.
func ParseArchiveLink(raw string) (ArchiveLink, error) {
	fail := func(why string) (ArchiveLink, error) {
		return ArchiveLink{}, fmt.Errorf("%w: %s (%s)", ErrNotArchiveLink, raw, why)
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fail("unparseable")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fail("scheme must be https")
	}
	host := strings.ToLower(u.Hostname())
	if host != "slack.com" && !strings.HasSuffix(host, ".slack.com") {
		return fail("host is not slack.com")
	}
	m := archivePathRe.FindStringSubmatch(u.Path)
	if m == nil || !isChannelID(m[1]) {
		return fail("path is not /archives/<channel>/p<ts>")
	}
	l := ArchiveLink{Channel: m[1]}
	if m[2] != "" {
		l.TS = m[2] + "." + m[3]
	}
	q := u.Query()
	if t := q.Get("thread_ts"); t != "" {
		if !slackTSRe.MatchString(t) {
			return fail("thread_ts is not a timestamp")
		}
		l.ThreadTS = t
	}
	if cid := q.Get("cid"); cid != "" && cid != l.Channel {
		return fail("cid names another channel")
	}
	return l, nil
}

// Parent is the ts of the thread the link belongs to: thread_ts when the
// link is to a reply, else the linked message.
func (l ArchiveLink) Parent() string {
	if l.ThreadTS != "" {
		return l.ThreadTS
	}
	return l.TS
}
