package feedcollect

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/infra/slack"
)

func TestSlackNoRetryTurnsClientRetryOff(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "9")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := &slack.Client{Token: "xoxp-test", Base: srv.URL + "/", Sleep: SlackNoRetry}
	var api SlackAPI = c
	_, err := api.HistoryPage(context.Background(), slack.HistoryQuery{Channel: "C1"})
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter != 9*time.Second {
		t.Fatalf("err = %v, want a RateLimitError with Retry-After 9s", err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("requests = %d, want 1 (no client retry)", n)
	}
}

func TestSlackHistoryResumesACutShortRead(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	start := slackTSOf(now)
	api := &pagedAPI{}
	s := NewSlackSource(SlackOptions{API: api, Self: "USELF", CallsPerMinute: 2, Now: func() time.Time { return now }}).(*slackSource)
	s.convs["D1"] = &slackConv{id: "D1", dm: true, start: start}
	p := &slackPoll{ctx: context.Background(), next: map[string]string{}, seen: map[string]bool{}}

	// Two pages fit the budget; the third waits, and the cursor holds the gap.
	api.pages = [][]slack.Message{{msgAt(now, 30), msgAt(now, 29)}, {msgAt(now, 20)}, {msgAt(now, 10)}}
	if ok, err := s.history(p, s.convs["D1"]); err != nil || ok {
		t.Fatalf("history = %v, %v; want out of budget", ok, err)
	}
	c := parseConvCursor(p.next["c:D1"], start)
	if c.ts != start || c.bound != slackTSOf(now.Add(20*time.Second)) || c.newest != slackTSOf(now.Add(30*time.Second)) {
		t.Fatalf("cursor = %+v", c)
	}
	now = now.Add(time.Minute)
	api.next = 2
	if ok, err := s.history(p, s.convs["D1"]); err != nil || !ok {
		t.Fatalf("history = %v, %v", ok, err)
	}
	if got := p.next["c:D1"]; got != slackTSOf(now.Add(-30*time.Second)) {
		t.Fatalf("cursor after the gap closed = %q", got)
	}
	if api.lastLatest != slackTSOf(now.Add(-40*time.Second)) {
		t.Fatalf("resumed read latest = %q, want the oldest message read", api.lastLatest)
	}
	if len(p.events) != 4 {
		t.Fatalf("events = %d, want 4", len(p.events))
	}
}

func msgAt(t time.Time, sec int) slack.Message {
	return slack.Message{Type: "message", User: "U2", TS: slackTSOf(t.Add(time.Duration(sec) * time.Second)), Text: "hi"}
}

type pagedAPI struct {
	SlackAPI
	pages      [][]slack.Message
	next       int
	lastLatest string
}

func (a *pagedAPI) HistoryPage(_ context.Context, q slack.HistoryQuery) (slack.Page[slack.Message], error) {
	a.lastLatest = q.Latest
	p := slack.Page[slack.Message]{Items: a.pages[a.next]}
	a.next++
	if a.next < len(a.pages) {
		p.NextCursor = "more"
	}
	return p, nil
}
