package feedcollect

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/slack"
)

// SlackAPI is the part of *slack.Client the Slack source reads through.
type SlackAPI interface {
	ChannelsPage(ctx context.Context, q slack.ChannelQuery) (slack.Page[slack.Channel], error)
	HistoryPage(ctx context.Context, q slack.HistoryQuery) (slack.Page[slack.Message], error)
	RepliesPage(ctx context.Context, q slack.RepliesQuery) (slack.Page[slack.Message], error)
	Search(ctx context.Context, q slack.SearchQuery) (slack.SearchResult, error)
}

// SlackOptions configure the Slack source. Self is the member's user id.
// CallsPerMinute (default 40) applies to listing, history and replies
// separately. Exclude names channels (id or name) never read; Bots lists
// the bot ids whose messages are kept. Sleep waits out a 429's Retry-After.
type SlackOptions struct {
	API            SlackAPI
	Self           string
	CallsPerMinute int
	Exclude        []string
	Bots           []string
	Now            func() time.Time
	Sleep          func(ctx context.Context, d time.Duration) error
}

// RateLimitError is a call Slack answered with HTTP 429.
type RateLimitError struct {
	Method     string
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("slack %s: rate limited, retry after %s", e.Method, e.RetryAfter)
}

// SlackNoRetry is a slack.Client Sleep that turns the client's own 429 retry
// off: the call returns a *RateLimitError carrying Retry-After, which the
// source counts and sleeps out itself.
func SlackNoRetry(_ context.Context, d time.Duration) error {
	return &RateLimitError{RetryAfter: d}
}

const (
	// SlackEvery is how often the collector polls the Slack source. Budgets
	// are per wall-clock minute, so extra polls in a minute only spend what
	// is left of it.
	SlackEvery = 30 * time.Second
	// DefaultSlackCallsPerMinute is the per-method budget when none is set.
	DefaultSlackCallsPerMinute = 40

	slackFullListEvery   = 10 * time.Minute
	slackActiveEvery     = 2 * time.Minute
	slackCoolingEvery    = 5 * time.Minute
	slackQuietEvery      = 15 * time.Minute
	slackRepliesPerMin   = 10
	slackListLimit       = 200
	slackHistoryLimit    = 200
	slackRepliesLimit    = 200
	slackSearchLimit     = 20
	slackThreadKeepFor   = 14 * 24 * time.Hour
	slackMineEvery       = 5 * time.Minute
	slackConvCursorPfx   = "c:"
	slackThreadCursorPfx = "t:"
	slackSearchCursor    = "search"
)

// Methods with their own budget.
const (
	slackMethodList    = "list"
	slackMethodHistory = "history"
	slackMethodReplies = "replies"
	slackMethodSearch  = "search"
)

// slackGoneCodes are API errors that mean a conversation or thread can no
// longer be read; the source forgets it instead of failing the poll.
var slackGoneCodes = []string{"channel_not_found", "not_in_channel", "is_archived", "thread_not_found", "missing_scope"}

type slackConv struct {
	id       string
	name     string
	dm       bool      // IM or group DM: every message is direct
	start    string    // ts at which the conversation was first listed
	lastRead time.Time // last history read (or first listing)
	active   time.Time // newest message seen
}

type slackSource struct {
	o       SlackOptions
	exclude map[string]bool
	bots    map[string]bool

	mu       sync.Mutex
	convs    map[string]*slackConv
	budget   map[string]budgetSlot
	lastList time.Time // minute of the last listing
	lastFull time.Time // time of the last complete full listing
	fullNext string    // page cursor of a full listing in progress
	fullSeen map[string]bool
	lastSrch time.Time // minute of the last mention search

	// Per thread cursor key: when its replies were last read, and whether a
	// history answer showed a newer latest_reply since.
	threadRead map[string]time.Time
	threadDue  map[string]bool
}

type budgetSlot struct {
	minute time.Time
	n      int
}

// NewSlackSource reads every conversation the member is in through api,
// inside a per-method call budget.
func NewSlackSource(o SlackOptions) Source {
	if o.CallsPerMinute <= 0 {
		o.CallsPerMinute = DefaultSlackCallsPerMinute
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Sleep == nil {
		o.Sleep = sleepCtx
	}
	s := &slackSource{
		o:       o,
		exclude: map[string]bool{},
		bots:    map[string]bool{},
		convs:   map[string]*slackConv{},
		budget:  map[string]budgetSlot{},

		threadRead: map[string]time.Time{},
		threadDue:  map[string]bool{},
	}
	for _, e := range o.Exclude {
		if e = strings.TrimPrefix(strings.TrimSpace(e), "#"); e != "" {
			s.exclude[strings.ToLower(e)] = true
		}
	}
	for _, b := range o.Bots {
		if b = strings.TrimSpace(b); b != "" {
			s.bots[b] = true
		}
	}
	return s
}

func (s *slackSource) Name() domain.FeedSource { return "slack" }
func (s *slackSource) Every() time.Duration    { return SlackEvery }

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// slackPoll is one Poll's working state: the cursors it will return and the
// events it made, deduplicated by external id.
type slackPoll struct {
	ctx    context.Context
	next   map[string]string
	mutes  []domain.FeedMute
	events []domain.FeedEvent
	seen   map[string]bool
}

func (p *slackPoll) add(ev domain.FeedEvent) {
	if p.seen[ev.ExternalID] {
		return
	}
	p.seen[ev.ExternalID] = true
	p.events = append(p.events, ev)
}

// Poll runs one pass in the minute's order: listing, DM history, the
// mention search, replies, channel history. A step out of budget waits for
// the next minute.
func (s *slackSource) Poll(ctx context.Context, cursors map[string]string, mutes []domain.FeedMute) ([]domain.FeedEvent, map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := &slackPoll{ctx: ctx, next: maps.Clone(cursors), mutes: mutes, seen: map[string]bool{}}
	if p.next == nil {
		p.next = map[string]string{}
	}
	if _, ok := p.next[slackSearchCursor]; !ok {
		p.next[slackSearchCursor] = slackTSOf(s.o.Now())
	}
	steps := []func(*slackPoll) error{s.list, s.dmHistory, s.searchMentions, s.replies, s.channelHistory}
	for _, step := range steps {
		if err := step(p); err != nil {
			return nil, nil, err
		}
	}
	for i := range p.events {
		p.events[i] = finish(p.events[i], s.Name())
	}
	return unmuted(p.events, mutes), p.next, nil
}

// call runs fn inside method's budget. It returns false when the budget is
// spent. Every call counts, a 429 included; a 429 sleeps its Retry-After and
// tries again while the budget lasts.
func (s *slackSource) call(ctx context.Context, method string, limit int, fn func() error) (bool, error) {
	for {
		now := s.o.Now()
		minute := now.Truncate(time.Minute)
		slot := s.budget[method]
		if !slot.minute.Equal(minute) {
			slot = budgetSlot{minute: minute}
		}
		if slot.n >= limit {
			s.budget[method] = slot
			return false, nil
		}
		slot.n++
		s.budget[method] = slot
		err := fn()
		var rl *RateLimitError
		if !errors.As(err, &rl) {
			return err == nil, err
		}
		if err := s.o.Sleep(ctx, rl.RetryAfter); err != nil {
			return false, err
		}
	}
}

func (s *slackSource) budgetFor(method string) int {
	if method == slackMethodSearch {
		return 1
	}
	return s.o.CallsPerMinute
}

func (s *slackSource) do(p *slackPoll, method string, fn func() error) (bool, error) {
	ok, err := s.call(p.ctx, method, s.budgetFor(method), fn)
	if err != nil {
		var rl *RateLimitError
		if errors.As(err, &rl) && rl.Method == "" {
			rl.Method = method
		}
	}
	return ok, err
}

// gone reports whether err says the conversation or thread cannot be read.
func gone(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, code := range slackGoneCodes {
		if strings.HasSuffix(msg, ": "+code) {
			return true
		}
	}
	return false
}

// excluded reports whether a conversation is never read.
func (s *slackSource) excluded(id, name string) bool {
	return s.exclude[strings.ToLower(id)] || (name != "" && s.exclude[strings.ToLower(name)])
}

func channelMuted(id string, mutes []domain.FeedMute) bool {
	for _, m := range mutes {
		if m.Scope == "channel" && m.Key == id {
			return true
		}
	}
	return false
}

func threadMuted(key string, mutes []domain.FeedMute) bool {
	for _, m := range mutes {
		if m.Scope == "thread" && m.Key == key {
			return true
		}
	}
	return false
}

// list runs once a minute: DMs and group DMs, or every type when the last
// complete full listing is 10 minutes old.
func (s *slackSource) list(p *slackPoll) error {
	now := s.o.Now()
	minute := now.Truncate(time.Minute)
	if s.lastList.Equal(minute) {
		return nil
	}
	full := s.lastFull.IsZero() || now.Sub(s.lastFull) >= slackFullListEvery || s.fullNext != ""
	if !full {
		var page slack.Page[slack.Channel]
		ok, err := s.do(p, slackMethodList, func() (err error) {
			page, err = s.o.API.ChannelsPage(p.ctx, slack.ChannelQuery{Mine: true, Types: []string{"im", "mpim"}, Limit: slackListLimit, ExcludeArchived: true})
			return err
		})
		if err != nil || !ok {
			return err
		}
		s.lastList = minute
		for _, c := range page.Items {
			s.learn(c, true, now)
		}
		return nil
	}
	if s.fullNext == "" {
		s.fullSeen = map[string]bool{}
	}
	for {
		var page slack.Page[slack.Channel]
		ok, err := s.do(p, slackMethodList, func() (err error) {
			page, err = s.o.API.ChannelsPage(p.ctx, slack.ChannelQuery{
				Mine: true, Types: []string{"public_channel", "private_channel", "mpim", "im"},
				Cursor: s.fullNext, Limit: slackListLimit, ExcludeArchived: true,
			})
			return err
		})
		if err != nil || !ok {
			return err // out of budget: the listing resumes next minute
		}
		s.lastList = minute
		for _, c := range page.Items {
			s.learn(c, c.IsIM || strings.HasPrefix(c.Name, "mpdm-"), now)
			s.fullSeen[c.ID] = true
		}
		s.fullNext = page.NextCursor
		if s.fullNext == "" {
			break
		}
	}
	for id := range s.convs {
		if !s.fullSeen[id] {
			delete(s.convs, id)
		}
	}
	s.fullSeen = nil
	s.lastFull = now
	return nil
}

// learn adds a listed conversation. A new one starts at now: nothing older
// is read.
func (s *slackSource) learn(c slack.Channel, dm bool, now time.Time) {
	if c.ID == "" {
		return
	}
	if cv, ok := s.convs[c.ID]; ok {
		if c.Name != "" {
			cv.name = c.Name
		}
		cv.dm = cv.dm || dm
		return
	}
	s.convs[c.ID] = &slackConv{id: c.ID, name: c.Name, dm: dm, start: slackTSOf(now), lastRead: now}
}

// convsBy returns the readable conversations of one kind, least recently
// read first.
func (s *slackSource) convsBy(dm bool, mutes []domain.FeedMute) []*slackConv {
	var out []*slackConv
	for _, c := range s.convs {
		if c.dm == dm && !s.excluded(c.id, c.name) && !channelMuted(c.id, mutes) {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b *slackConv) int {
		if c := a.lastRead.Compare(b.lastRead); c != 0 {
			return c
		}
		return strings.Compare(a.id, b.id)
	})
	return out
}

// dmHistory reads each DM and group DM once a minute. DMs never wait for
// channels: they spend the history budget first.
func (s *slackSource) dmHistory(p *slackPoll) error {
	minute := s.o.Now().Truncate(time.Minute)
	for _, c := range s.convsBy(true, p.mutes) {
		if !c.lastRead.Truncate(time.Minute).Before(minute) {
			continue
		}
		ok, err := s.history(p, c)
		if err != nil || !ok {
			return err
		}
	}
	return nil
}

// channelInterval is how long a channel waits between reads: every 2
// minutes while it had a message in the last hour, backing off to 15.
func channelInterval(c *slackConv, now time.Time) time.Duration {
	switch since := now.Sub(c.active); {
	case c.active.IsZero():
		return slackQuietEvery
	case since < time.Hour:
		return slackActiveEvery
	case since < 2*time.Hour:
		return slackCoolingEvery
	default:
		return slackQuietEvery
	}
}

// channelHistory reads the channels that are due, most recently active
// first, while the history budget lasts.
func (s *slackSource) channelHistory(p *slackPoll) error {
	now := s.o.Now()
	var due []*slackConv
	for _, c := range s.convsBy(false, p.mutes) {
		if now.Sub(c.lastRead) >= channelInterval(c, now) {
			due = append(due, c)
		}
	}
	slices.SortStableFunc(due, func(a, b *slackConv) int { return b.active.Compare(a.active) })
	for _, c := range due {
		ok, err := s.history(p, c)
		if err != nil || !ok {
			return err
		}
	}
	return nil
}

// history reads one conversation's new messages, page by page while the
// budget lasts. It returns false when the budget ran out. A read cut short
// keeps its place: the next read asks only for what is older than the
// oldest message it got, and the cursor moves once the gap is closed.
func (s *slackSource) history(p *slackPoll, c *slackConv) (bool, error) {
	key := slackConvCursorPfx + c.id
	cur := parseConvCursor(p.next[key], c.start)
	latest := cur.bound // fixed for the whole page run
	page := ""
	for {
		var res slack.Page[slack.Message]
		ok, err := s.do(p, slackMethodHistory, func() (err error) {
			res, err = s.o.API.HistoryPage(p.ctx, slack.HistoryQuery{Channel: c.id, Oldest: cur.ts, Latest: latest, Cursor: page, Limit: slackHistoryLimit})
			return err
		})
		if gone(err) {
			delete(s.convs, c.id)
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if !ok {
			if page != "" {
				p.next[key] = cur.String()
			}
			return false, nil
		}
		c.lastRead = s.o.Now()
		for _, m := range res.Items {
			if !tsAfter(m.TS, cur.ts) {
				continue
			}
			if tsAfter(m.TS, cur.newest) {
				cur.newest = m.TS
			}
			if cur.bound == "" || tsAfter(cur.bound, m.TS) {
				cur.bound = m.TS
			}
			if at := slackTime(m.TS); at.After(c.active) {
				c.active = at
			}
			s.fromHistory(p, c, m)
		}
		page = res.NextCursor
		if page == "" {
			break
		}
	}
	if tsAfter(cur.newest, cur.ts) {
		cur.ts = cur.newest
	}
	cur.bound, cur.newest = "", ""
	p.next[key] = cur.String()
	return true, nil
}

// fromHistory turns one history message into an event and notes a thread
// whose reply count grew.
func (s *slackSource) fromHistory(p *slackPoll, c *slackConv, m slack.Message) {
	threadTS := m.Thread
	if threadTS == m.TS {
		threadTS = ""
	}
	if m.ReplyCount > 0 && threadTS == "" {
		tkey := slackThreadCursorPfx + c.id + "/" + m.TS
		t := parseThreadCursor(p.next[tkey], m.TS)
		if m.ReplyCount > t.seen {
			t.seen = m.ReplyCount
		}
		if m.User == s.o.Self {
			t.mine = true
		}
		if tsAfter(m.LatestReply, t.latest) {
			if t.latest != "" || tsAfter(m.LatestReply, t.last) {
				s.threadDue[tkey] = true
			}
			t.latest = m.LatestReply
		}
		p.next[tkey] = t.String()
	}
	if threadTS != "" {
		if tkey := slackThreadCursorPfx + c.id + "/" + threadTS; m.User == s.o.Self {
			if t, ok := p.next[tkey]; ok {
				tc := parseThreadCursor(t, threadTS)
				tc.mine = true
				p.next[tkey] = tc.String()
			}
		}
	}
	if ev, ok := s.messageEvent(c, m, threadTS, false, ""); ok {
		p.add(ev)
	}
}

// searchMentions runs one mention search a minute and keeps hits newer than
// the search cursor in conversations the history has not reached yet.
func (s *slackSource) searchMentions(p *slackPoll) error {
	minute := s.o.Now().Truncate(time.Minute)
	if s.lastSrch.Equal(minute) || s.o.Self == "" {
		return nil
	}
	since := p.next[slackSearchCursor]
	q := slack.SearchQuery{Query: "<@" + s.o.Self + ">", Private: true, Limit: slackSearchLimit}
	if at := slackTime(since); !at.IsZero() {
		q.After = at.Add(-24 * time.Hour).UTC().Format("2006-01-02")
	}
	var res slack.SearchResult
	ok, err := s.do(p, slackMethodSearch, func() (err error) {
		res, err = s.o.API.Search(p.ctx, q)
		return err
	})
	if err != nil || !ok {
		return err
	}
	s.lastSrch = minute
	newest := since
	for _, h := range res.Hits {
		if h.Kind != "message" || h.Channel == "" || h.TS == "" || !tsAfter(h.TS, since) {
			continue
		}
		if tsAfter(h.TS, newest) {
			newest = h.TS
		}
		if h.User == s.o.Self || s.excluded(h.Channel, h.Name) || channelMuted(h.Channel, p.mutes) {
			continue
		}
		c := s.convs[h.Channel]
		if c != nil && !tsAfter(h.TS, parseConvCursor(p.next[slackConvCursorPfx+c.id], c.start).ts) {
			continue // history already read it
		}
		p.add(s.searchEvent(c, h))
	}
	p.next[slackSearchCursor] = newest
	return nil
}

// replies reads the threads that are due, newest activity first, at most 10
// a minute: a thread whose reply count or latest reply grew, and each of the
// member's threads every 5 minutes. A thread with no activity for 14 days is
// forgotten: its cursor leaves the map. A muted thread is not read.
func (s *slackSource) replies(p *slackPoll) error {
	type pending struct {
		key, channel, ts string
		t                threadCursor
	}
	now := s.o.Now()
	cutoff := now.Add(-slackThreadKeepFor)
	var due []pending
	for k, v := range p.next {
		rest, ok := strings.CutPrefix(k, slackThreadCursorPfx)
		if !ok {
			continue
		}
		ch, ts, ok := strings.Cut(rest, "/")
		if !ok {
			continue
		}
		t := parseThreadCursor(v, ts)
		if slackTime(t.activity()).Before(cutoff) {
			delete(p.next, k)
			delete(s.threadRead, k)
			delete(s.threadDue, k)
			continue
		}
		grew := t.seen > t.read || s.threadDue[k]
		stale := t.mine && now.Sub(s.threadRead[k]) >= slackMineEvery
		if !grew && !stale {
			continue
		}
		c := s.convs[ch]
		if c == nil || s.excluded(c.id, c.name) || channelMuted(ch, p.mutes) || threadMuted(domain.FeedThreadKeySlack(ch, ts, ts), p.mutes) {
			continue
		}
		due = append(due, pending{key: k, channel: ch, ts: ts, t: t})
	}
	slices.SortFunc(due, func(a, b pending) int {
		if c := tsCompare(b.t.activity(), a.t.activity()); c != 0 {
			return c
		}
		if c := tsCompare(b.ts, a.ts); c != 0 {
			return c
		}
		return strings.Compare(a.channel, b.channel)
	})
	minute := now.Truncate(time.Minute)
	for _, d := range due {
		if slot := s.budget["replies-threads"]; slot.minute.Equal(minute) && slot.n >= slackRepliesPerMin {
			return nil
		}
		ok, err := s.thread(p, s.convs[d.channel], d.key, d.t)
		if err != nil || !ok {
			return err
		}
		s.threadRead[d.key] = s.o.Now()
		delete(s.threadDue, d.key)
		slot := s.budget["replies-threads"]
		if !slot.minute.Equal(minute) {
			slot = budgetSlot{minute: minute}
		}
		slot.n++
		s.budget["replies-threads"] = slot
	}
	return nil
}

// thread reads one thread's replies after the last one read, oldest first.
func (s *slackSource) thread(p *slackPoll, c *slackConv, key string, t threadCursor) (bool, error) {
	page := ""
	var before []slack.Message
	for {
		var res slack.Page[slack.Message]
		ok, err := s.do(p, slackMethodReplies, func() (err error) {
			res, err = s.o.API.RepliesPage(p.ctx, slack.RepliesQuery{Channel: c.id, TS: t.parent, Oldest: t.last, Cursor: page, Limit: slackRepliesLimit})
			return err
		})
		if gone(err) {
			delete(p.next, key) // the thread cannot be read again
			return true, nil
		}
		if err != nil || !ok {
			p.next[key] = t.String()
			return ok, err
		}
		for _, m := range res.Items {
			if m.User == s.o.Self && m.TS != t.parent {
				t.mine = true
			}
		}
		for _, m := range res.Items {
			if m.TS == t.parent {
				before = append(before, m)
				continue
			}
			if !tsAfter(m.TS, t.last) {
				continue
			}
			t.last = m.TS
			if ev, ok := s.messageEvent(c, m, t.parent, t.mine, threadContext(before)); ok {
				p.add(ev)
			}
			before = append(before, m)
		}
		page = res.NextCursor
		if page == "" {
			break
		}
	}
	t.read = t.seen
	p.next[key] = t.String()
	return true, nil
}
