//go:build feedaccept || feedaccept_p3b

package feedcollect_test

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/feedcollect"
	"github.com/bouwerp/aiman/internal/infra/slack"
)

// Acceptance tests for the Slack source (P3b, design "Slack reading").

const self = "USELF"

func slackTS(at time.Time, n int) string { return fmt.Sprintf("%d.%06d", at.Unix(), n) }

type slackCall struct {
	method  string // list, history, replies, search
	channel string
	ts      string
	types   []string
}

// fakeSlack is SlackAPI over fixed conversations. History and replies answer
// messages newer than Oldest, newest first; a method in limited answers
// RateLimitError that many times before it works.
type fakeSlack struct {
	mu       sync.Mutex
	convs    []slack.Channel
	history  map[string][]slack.Message // channel -> messages
	replies  map[string][]slack.Message // "<channel>/<parent ts>" -> parent then replies
	mentions []slack.SearchHit
	limited  map[string]int
	retry    time.Duration
	calls    []slackCall
}

func newFakeSlack(convs ...slack.Channel) *fakeSlack {
	return &fakeSlack{convs: convs, history: map[string][]slack.Message{}, replies: map[string][]slack.Message{}, limited: map[string]int{}}
}

func im(id string) slack.Channel { return slack.Channel{ID: id, IsIM: true, IsMember: true} }
func channel(id string) slack.Channel {
	return slack.Channel{ID: id, Name: "chan-" + id, IsMember: true}
}

func (f *fakeSlack) post(ch string, m slack.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.history[ch] = append(f.history[ch], m)
}

func (f *fakeSlack) reply(ch, parent string, m slack.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := ch + "/" + parent
	if len(f.replies[key]) == 0 {
		for _, p := range f.history[ch] {
			if p.TS == parent {
				f.replies[key] = append(f.replies[key], p)
			}
		}
	}
	m.Thread = parent
	f.replies[key] = append(f.replies[key], m)
}

func (f *fakeSlack) limit(method string) error {
	if f.limited[method] > 0 {
		f.limited[method]--
		return &feedcollect.RateLimitError{Method: method, RetryAfter: f.retry}
	}
	return nil
}

func newer(ms []slack.Message, oldest string) []slack.Message {
	var out []slack.Message
	for _, m := range ms {
		if oldest == "" || m.TS > oldest {
			out = append(out, m)
		}
	}
	slices.Reverse(out)
	return out
}

func (f *fakeSlack) ChannelsPage(_ context.Context, q slack.ChannelQuery) (slack.Page[slack.Channel], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, slackCall{method: "list", types: q.Types})
	if err := f.limit("list"); err != nil {
		return slack.Page[slack.Channel]{}, err
	}
	dmsOnly := !slices.Contains(q.Types, "public_channel") && !slices.Contains(q.Types, "private_channel") && len(q.Types) > 0
	var out []slack.Channel
	for _, c := range f.convs {
		if !dmsOnly || c.IsIM {
			out = append(out, c)
		}
	}
	return slack.Page[slack.Channel]{Items: out}, nil
}

func (f *fakeSlack) HistoryPage(_ context.Context, q slack.HistoryQuery) (slack.Page[slack.Message], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, slackCall{method: "history", channel: q.Channel})
	if err := f.limit("history"); err != nil {
		return slack.Page[slack.Message]{}, err
	}
	return slack.Page[slack.Message]{Items: newer(f.history[q.Channel], q.Oldest)}, nil
}

func (f *fakeSlack) RepliesPage(_ context.Context, q slack.RepliesQuery) (slack.Page[slack.Message], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, slackCall{method: "replies", channel: q.Channel, ts: q.TS})
	if err := f.limit("replies"); err != nil {
		return slack.Page[slack.Message]{}, err
	}
	all := f.replies[q.Channel+"/"+q.TS]
	out := newer(all, q.Oldest)
	slices.Reverse(out) // replies come oldest first
	return slack.Page[slack.Message]{Items: out}, nil
}

func (f *fakeSlack) Search(_ context.Context, q slack.SearchQuery) (slack.SearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, slackCall{method: "search"})
	if err := f.limit("search"); err != nil {
		return slack.SearchResult{}, err
	}
	if !strings.Contains(q.Query, "<@"+self+">") {
		return slack.SearchResult{Engine: "realtime"}, nil
	}
	return slack.SearchResult{Engine: "realtime", Hits: append([]slack.SearchHit(nil), f.mentions...)}, nil
}

// takeCalls returns the calls since the last take.
func (f *fakeSlack) takeCalls() []slackCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.calls
	f.calls = nil
	return out
}

type slackEnv struct {
	api     *fakeSlack
	clock   *clock
	sleeps  []time.Duration
	src     feedcollect.Source
	cursors map[string]string
}

func newSlackEnv(t *testing.T, api *fakeSlack, perMinute int, exclude, bots []string) *slackEnv {
	t.Helper()
	env := &slackEnv{api: api, clock: newClock(collectT0), cursors: map[string]string{}}
	env.src = feedcollect.NewSlackSource(feedcollect.SlackOptions{
		API: api, Self: self, CallsPerMinute: perMinute, Exclude: exclude, Bots: bots,
		Now: env.clock.Now,
		Sleep: func(_ context.Context, d time.Duration) error {
			env.sleeps = append(env.sleeps, d)
			env.clock.Add(d)
			return nil
		},
	})
	if env.src.Name() != "slack" {
		t.Fatalf("name = %q", env.src.Name())
	}
	// The first poll lists every conversation; each starts at that time.
	env.poll(t, nil)
	api.takeCalls()
	return env
}

func (e *slackEnv) poll(t *testing.T, mutes []domain.FeedMute) []domain.FeedEvent {
	t.Helper()
	evs, next, err := e.src.Poll(context.Background(), maps.Clone(e.cursors), mutes)
	if err != nil {
		t.Fatal(err)
	}
	e.cursors = next
	return evs
}

// at moves the clock to collectT0 plus d.
func (e *slackEnv) at(d time.Duration) { e.clock.Add(collectT0.Add(d).Sub(e.clock.Now())) }

func msg(user, ts, text string) slack.Message {
	return slack.Message{Type: "message", User: user, TS: ts, Text: text}
}

func eventsFor(evs []domain.FeedEvent, ext string) []domain.FeedEvent {
	var out []domain.FeedEvent
	for _, ev := range evs {
		if ev.ExternalID == ext {
			out = append(out, ev)
		}
	}
	return out
}

func TestSlackClassifiesFiltersAndRedacts(t *testing.T) {
	api := newFakeSlack(im("D1"), channel("C1"))
	env := newSlackEnv(t, api, 40, nil, []string{"BOK"})
	at := collectT0.Add(30 * time.Second)
	ts := func(n int) string { return slackTS(at, n) }

	api.post("D1", msg("U2", ts(1), "can you check the deploy? token xoxb-1234567890-0987654321-abcdefABCDEF"))
	api.post("D1", msg(self, ts(2), "my own top-level DM"))
	api.post("C1", msg("U3", ts(3), "lunch anyone"))
	api.post("C1", msg("U3", ts(4), "<@"+self+"> please review"))
	api.post("C1", slack.Message{Type: "message", Subtype: "channel_join", User: "U5", TS: ts(5), Text: "<@U5> has joined"})
	api.post("C1", slack.Message{Type: "message", Subtype: "channel_leave", User: "U5", TS: ts(6), Text: "<@U5> has left"})
	api.post("C1", slack.Message{Type: "message", Subtype: "channel_topic", User: "U5", TS: ts(7), Text: "set the topic"})
	api.post("C1", slack.Message{Type: "message", Subtype: "bot_message", BotID: "BNOISE", TS: ts(8), Text: "build passed"})
	api.post("C1", slack.Message{Type: "message", Subtype: "bot_message", BotID: "BOK", TS: ts(9), Text: "pager: db down"})
	api.post("C1", msg(self, ts(10), "my own top-level channel message"))
	api.post("C1", msg("U3", ts(11), strings.Repeat("a", 10000)))
	api.post("C1", slack.Message{Type: "message", User: "U3", TS: ts(12), Text: "thread parent", ReplyCount: 2})
	api.reply("C1", ts(12), msg(self, ts(13), "my reply"))
	api.reply("C1", ts(12), msg("U4", ts(14), "answer to you"))
	api.mentions = []slack.SearchHit{{Kind: "message", Channel: "C1", TS: ts(4), User: "U3", Text: "<@" + self + "> please review"}}

	// Every conversation is due 16 minutes on; the next minute reads the
	// replies the channel history announced.
	env.at(16 * time.Minute)
	evs := env.poll(t, nil)
	env.at(17 * time.Minute)
	evs = append(evs, env.poll(t, nil)...)

	cases := []struct {
		ext, thread, class string
		urgent, own        bool
	}{
		{"D1/" + ts(1), "slack:D1/" + ts(1), "direct", true, false},
		{"C1/" + ts(3), "slack:C1/" + ts(3), "ambient", false, false},
		{"C1/" + ts(4), "slack:C1/" + ts(4), "direct", true, false},
		{"C1/" + ts(9), "slack:C1/" + ts(9), "ambient", false, false},
		{"C1/" + ts(12), "slack:C1/" + ts(12), "ambient", false, false},
		{"C1/" + ts(13), "slack:C1/" + ts(12), "", false, true}, // own reply: class not pinned
		{"C1/" + ts(14), "slack:C1/" + ts(12), "direct", true, false},
	}
	for _, c := range cases {
		got := eventsFor(evs, c.ext)
		if len(got) == 0 {
			t.Errorf("%s missing", c.ext)
			continue
		}
		for _, ev := range got {
			if ev.Source != "slack" || ev.Kind != "message" || ev.ThreadKey != c.thread || (c.class != "" && string(ev.Class) != c.class) ||
				ev.Urgent != c.urgent || ev.Own != c.own || ev.At.IsZero() || ev.Actor == "" {
				t.Errorf("%s = %+v, want thread %s class %s urgent %v own %v", c.ext, ev, c.thread, c.class, c.urgent, c.own)
			}
		}
	}
	if ev := eventsFor(evs, "C1/"+ts(3)); len(ev) > 0 && ev[0].Channel != "C1" {
		t.Errorf("ambient event channel = %q, want C1", ev[0].Channel)
	}
	for _, n := range []int{2, 5, 6, 7, 8, 10} {
		for _, ch := range []string{"D1", "C1"} {
			if got := eventsFor(evs, ch+"/"+ts(n)); len(got) > 0 {
				t.Errorf("filtered message %s/%s was kept: %+v", ch, ts(n), got[0])
			}
		}
	}
	if ev := eventsFor(evs, "D1/"+ts(1)); len(ev) > 0 && strings.Contains(ev[0].Body, "xoxb-1234567890") {
		t.Errorf("body not redacted: %q", ev[0].Body)
	}
	if ev := eventsFor(evs, "C1/"+ts(11)); len(ev) == 0 || len(ev[0].Body) > 4096 {
		t.Errorf("long body not stored cut to 4 KiB: %d events", len(ev))
	}
}

// rank is the place of a call in the minute's order: listing, DM history,
// mention search, replies, channel history.
func rank(c slackCall) int {
	switch {
	case c.method == "list":
		return 0
	case c.method == "history" && strings.HasPrefix(c.channel, "D"):
		return 1
	case c.method == "search":
		return 2
	case c.method == "replies":
		return 3
	default:
		return 4
	}
}

func TestSlackOrderInsideAMinute(t *testing.T) {
	api := newFakeSlack(im("D1"), channel("C1"), channel("C2"))
	env := newSlackEnv(t, api, 40, nil, nil)
	at := collectT0.Add(time.Minute)
	api.post("D1", slack.Message{Type: "message", User: "U2", TS: slackTS(at, 1), Text: "parent", ReplyCount: 1})
	api.reply("D1", slackTS(at, 1), msg("U2", slackTS(at, 2), "reply"))
	api.post("C1", msg("U3", slackTS(at, 3), "news"))
	api.post("C2", msg("U3", slackTS(at, 4), "more news"))

	env.at(16 * time.Minute)
	env.poll(t, nil)
	calls := api.takeCalls()
	seen := map[int]int{}
	for i, c := range calls {
		seen[rank(c)]++
		if i > 0 && rank(c) < rank(calls[i-1]) {
			t.Fatalf("call %d %+v comes after %+v; calls %+v", i, c, calls[i-1], calls)
		}
	}
	if seen[0] == 0 || seen[1] == 0 || seen[3] == 0 || seen[4] == 0 {
		t.Fatalf("a step is missing: %+v", calls)
	}
	if seen[2] != 1 {
		t.Fatalf("mention searches in one minute = %d, want 1", seen[2])
	}
}

func TestSlackDMListingEveryMinuteFullListingEveryTenMinutes(t *testing.T) {
	api := newFakeSlack(im("D1"), channel("C1"))
	env := newSlackEnv(t, api, 40, nil, nil)
	full := func(c slackCall) bool { return slices.Contains(c.types, "public_channel") }
	for minute := 1; minute <= 11; minute++ {
		env.at(time.Duration(minute) * time.Minute)
		env.poll(t, nil)
		var lists []slackCall
		for _, c := range api.takeCalls() {
			if c.method == "list" {
				lists = append(lists, c)
			}
		}
		if len(lists) == 0 {
			t.Fatalf("minute %d: no listing", minute)
		}
		anyFull := slices.ContainsFunc(lists, full)
		for _, c := range lists {
			if !full(c) && (!slices.Contains(c.types, "im") || !slices.Contains(c.types, "mpim")) {
				t.Fatalf("minute %d: DM listing types = %v", minute, c.types)
			}
		}
		if wantFull := minute == 10; anyFull != wantFull {
			t.Fatalf("minute %d: full listing = %v, want %v", minute, anyFull, wantFull)
		}
	}
}

func TestSlackNewConversationStartsWhenFirstListed(t *testing.T) {
	api := newFakeSlack(im("D1"))
	env := newSlackEnv(t, api, 40, nil, nil)
	// D2 has history from before it was listed, and one message after.
	api.mu.Lock()
	api.convs = append(api.convs, im("D2"))
	api.mu.Unlock()
	api.post("D2", msg("U2", slackTS(collectT0.Add(-time.Hour), 1), "old"))
	env.at(time.Minute)
	evs := env.poll(t, nil)
	api.post("D2", msg("U2", slackTS(collectT0.Add(90*time.Second), 1), "new"))
	env.at(2 * time.Minute)
	evs = append(evs, env.poll(t, nil)...)
	if len(eventsFor(evs, "D2/"+slackTS(collectT0.Add(-time.Hour), 1))) != 0 {
		t.Fatal("a new conversation replayed history from before it was listed")
	}
	if len(eventsFor(evs, "D2/"+slackTS(collectT0.Add(90*time.Second), 1))) != 1 {
		t.Fatalf("new conversation's message missing: %+v", evs)
	}
}

func TestSlackRepliesOnlyForThreadsAndAtMostTenAMinute(t *testing.T) {
	api := newFakeSlack(im("D1"))
	env := newSlackEnv(t, api, 40, nil, nil)
	at := collectT0.Add(30 * time.Second)
	api.post("D1", msg("U2", slackTS(at, 99), "no thread here"))
	for i := range 12 {
		p := slackTS(at, i+1)
		api.post("D1", slack.Message{Type: "message", User: "U2", TS: p, Text: "parent", ReplyCount: 1})
		api.reply("D1", p, msg("U2", slackTS(at, 50+i), "reply"))
	}
	replies := func() map[string]int {
		out := map[string]int{}
		for _, c := range api.takeCalls() {
			if c.method == "replies" {
				out[c.ts]++
			}
		}
		return out
	}
	env.at(time.Minute)
	env.poll(t, nil)
	first := replies()
	if n := len(first); n == 0 || n > 10 {
		t.Fatalf("replies in one minute = %d, want 1 to 10", n)
	}
	if first[slackTS(at, 99)] != 0 {
		t.Fatal("replies read for a message with no replies")
	}
	env.at(2 * time.Minute)
	env.poll(t, nil)
	second := replies()
	for ts := range first {
		if second[ts] != 0 {
			t.Errorf("unchanged thread %s read again", ts)
		}
	}
	if len(first)+len(second) != 12 {
		t.Fatalf("threads read over two minutes = %d, want 12", len(first)+len(second))
	}
}

func historyCalls(calls []slackCall) []string {
	var out []string
	for _, c := range calls {
		if c.method == "history" {
			out = append(out, c.channel)
		}
	}
	return out
}

func TestSlackBudgetCountsEveryCallIncluding429(t *testing.T) {
	api := newFakeSlack(im("D1"), im("D2"), im("D3"), im("D4"), im("D5"))
	env := newSlackEnv(t, api, 3, nil, nil)
	at := collectT0.Add(30 * time.Second)
	for i, d := range []string{"D1", "D2", "D3", "D4", "D5"} {
		api.post(d, msg("U2", slackTS(at, i+1), "hi"))
	}
	api.limited["history"] = 1
	api.retry = 7 * time.Second

	env.at(90 * time.Second)
	env.poll(t, nil)
	if n := len(historyCalls(api.takeCalls())); n == 0 || n > 3 {
		t.Fatalf("history calls in the minute = %d, want 1 to 3 counting the 429", n)
	}
	if !slices.Contains(env.sleeps, 7*time.Second) {
		t.Fatalf("sleeps = %v, want the 429's Retry-After", env.sleeps)
	}

	// The budget is per minute, not per poll.
	env.at(110 * time.Second)
	env.poll(t, nil)
	if n := len(historyCalls(api.takeCalls())); n != 0 {
		t.Fatalf("history calls later in the same minute = %d, want 0", n)
	}
	env.at(3 * time.Minute)
	env.poll(t, nil)
	if n := len(historyCalls(api.takeCalls())); n == 0 || n > 3 {
		t.Fatalf("history calls in the next minute = %d, want 1 to 3", n)
	}
}

func TestSlackDMsNeverWaitForChannels(t *testing.T) {
	api := newFakeSlack(channel("C1"), channel("C2"), channel("C3"), im("D1"))
	env := newSlackEnv(t, api, 1, nil, nil)
	at := collectT0.Add(15 * time.Minute)
	for _, c := range []string{"C1", "C2", "C3"} {
		api.post(c, msg("U3", slackTS(at, 1), "chatter"))
	}
	api.post("D1", msg("U2", slackTS(at, 2), "ping"))

	env.at(16 * time.Minute)
	evs := env.poll(t, nil)
	if got := historyCalls(api.takeCalls()); !slices.Equal(got, []string{"D1"}) {
		t.Fatalf("history calls with a budget of 1 = %v, want only D1", got)
	}
	if len(eventsFor(evs, "D1/"+slackTS(at, 2))) != 1 {
		t.Fatalf("DM message missing: %+v", evs)
	}
}

func TestSlackMutesAndExclusions(t *testing.T) {
	api := newFakeSlack(channel("C1"), channel("C2"), channel("C3"))
	env := newSlackEnv(t, api, 40, []string{"C2"}, nil)
	at := collectT0.Add(time.Minute)
	muted, open := slackTS(at, 1), slackTS(at, 2)
	api.post("C1", slack.Message{Type: "message", User: "U3", TS: muted, Text: "muted thread", ReplyCount: 1})
	api.reply("C1", muted, msg("U4", slackTS(at, 3), "muted reply"))
	api.post("C1", slack.Message{Type: "message", User: "U3", TS: open, Text: "open thread", ReplyCount: 1})
	api.reply("C1", open, msg("U4", slackTS(at, 4), "open reply"))
	api.post("C2", msg("U3", slackTS(at, 5), "excluded"))
	api.post("C3", msg("U3", slackTS(at, 6), "muted channel"))
	api.mentions = []slack.SearchHit{{Kind: "message", Channel: "C2", TS: slackTS(at, 7), User: "U3", Text: "<@" + self + "> in an excluded channel"}}
	mutes := []domain.FeedMute{
		{Scope: "channel", Key: "C3", Until: collectT0.Add(time.Hour)},
		{Scope: "thread", Key: "slack:C1/" + muted, Until: collectT0.Add(time.Hour)},
	}

	var evs []domain.FeedEvent
	var calls []slackCall
	for _, m := range []time.Duration{16, 17, 18} {
		env.at(m * time.Minute)
		evs = append(evs, env.poll(t, mutes)...)
		calls = append(calls, api.takeCalls()...)
	}
	for _, c := range calls {
		if (c.method == "history" || c.method == "replies") && (c.channel == "C2" || c.channel == "C3") {
			t.Errorf("read %s of %s", c.method, c.channel)
		}
		if c.method == "replies" && c.ts == muted {
			t.Errorf("read replies of the muted thread")
		}
	}
	if !slices.ContainsFunc(calls, func(c slackCall) bool { return c.method == "replies" && c.ts == open }) {
		t.Errorf("replies of the open thread never read: %+v", calls)
	}
	for _, ev := range evs {
		if ev.Channel == "C2" || ev.Channel == "C3" || strings.HasPrefix(ev.ExternalID, "C2/") || strings.HasPrefix(ev.ExternalID, "C3/") {
			t.Errorf("event from an excluded or muted channel: %+v", ev)
		}
		if ev.ThreadKey == "slack:C1/"+muted {
			t.Errorf("event from the muted thread: %+v", ev)
		}
	}
	if len(eventsFor(evs, "C1/"+slackTS(at, 4))) == 0 {
		t.Errorf("open thread reply missing: %+v", evs)
	}
}
