//go:build feedaccept || feedaccept_p10

package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/usecase"
)

// Acceptance tests for event-feed piece P10 (docs/designs/event-feed.md
// Surfaces, mockups M1, M2, M2b; docs/specs/event-feed.md §9).

const feedHost = "10.0.1.5"

type feedCall struct {
	method, host string
	params       map[string]any
}

// feedFake answers feed.* RPCs for one host and records every call.
type feedFake struct {
	mu      sync.Mutex
	calls   []feedCall
	actions []domain.FeedAction
	events  map[int64]domain.FeedEvent
}

func (f *feedFake) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.method == method {
			n++
		}
	}
	return n
}

func (f *feedFake) lastAct(t *testing.T) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].method == "feed.act" {
			return f.calls[i].params
		}
	}
	t.Fatalf("no feed.act call; saw %+v", f.calls)
	return nil
}

func (f *feedFake) answer(method string, p map[string]any) (any, error) {
	switch method {
	case "feed.status":
		high := 0
		for _, a := range f.actions {
			if a.Priority == "high" {
				high++
			}
		}
		return map[string]any{
			"type": "feed_status", "enabled": true,
			"collector":  map[string]any{"state": "running", "health": []any{}},
			"high_water": 820, "pending": 0, "open_actions": len(f.actions), "high_actions": high,
			"last_batch":    domain.FeedBatch{ID: 37, State: "done", Agent: "claude", EndedAt: time.Date(2026, 11, 26, 10, 4, 0, 0, time.Local)},
			"batches_today": 3, "settings": map[string]any{},
		}, nil
	case "feed.actions":
		return map[string]any{"type": "feed_actions", "actions": f.actions}, nil
	case "feed.events":
		var out []domain.FeedEvent
		ids, _ := p["ids"].([]any)
		for _, id := range ids {
			if n, ok := id.(float64); ok {
				if ev, ok := f.events[int64(n)]; ok {
					out = append(out, ev)
				}
			}
		}
		if len(ids) == 0 {
			for _, ev := range f.events {
				out = append(out, ev)
			}
		}
		return map[string]any{"type": "feed_events", "events": out}, nil
	case "feed.memory":
		return map[string]any{"type": "feed_memory", "bullets": []any{}, "mutes": []any{}}, nil
	case "feed.act":
		a := f.actions[0]
		a.State = "done"
		return map[string]any{"type": "feed_action", "action": a}, nil
	}
	return nil, &usecase.RPCError{Code: server.CodeInvalidParams, Message: "unknown method " + method}
}

func installFeedFake(t *testing.T, f *feedFake) {
	t.Helper()
	prev := feedRPC
	feedRPC = func(_ context.Context, r config.Remote, method string, params any) (json.RawMessage, error) {
		var p map[string]any
		b, _ := json.Marshal(params)
		_ = json.Unmarshal(b, &p)
		f.mu.Lock()
		f.calls = append(f.calls, feedCall{method: method, host: r.Host, params: p})
		res, err := f.answer(method, p)
		f.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return json.Marshal(res)
	}
	t.Cleanup(func() { feedRPC = prev })
}

func feedOpt(label, text string) domain.FeedOption {
	return domain.FeedOption{Label: label, Params: json.RawMessage(fmt.Sprintf(`{"channel":"C1","thread_ts":"1700.1","text":%q}`, text))}
}

func feedAnswers(id int64, at time.Time, excerpt string) domain.FeedTraceEntry {
	return domain.FeedTraceEntry{Kind: "event", Ref: fmt.Sprint(id), At: at, Role: "answers", Excerpt: excerpt}
}

// sampleFeed is the scenario of mockups M1 and M2: four open actions, one
// high. Action 12's trace has eight entries, the last "trace-eight".
func sampleFeed() *feedFake {
	at := time.Date(2026, 11, 26, 10, 2, 0, 0, time.Local)
	trace := []domain.FeedTraceEntry{
		{Kind: "event", Ref: "790", At: at.Add(-18 * time.Hour), Role: "context", Excerpt: "trace-one Ana starts the thread"},
		{Kind: "event", Ref: "795", At: at.Add(-17 * time.Hour), Role: "context", Excerpt: "trace-two is the threshold moving?"},
		feedAnswers(811, at, "Ana: does the settler config change break the bot?"),
		{Kind: "context", Ref: "wtb-2628", At: at, Role: "context", Excerpt: "trace-four wtb-2628 to-do: fork test"},
		{Kind: "memory", Ref: "m12", At: at.Add(-time.Hour), Role: "cause", Excerpt: "trace-five Ana asks about settler changes"},
		{Kind: "decision", Ref: "40", At: at.Add(-2 * time.Hour), Role: "context", Excerpt: "trace-six approved a reply to Ana"},
		{Kind: "event", Ref: "799", At: at.Add(-3 * time.Hour), Role: "context", Excerpt: "trace-seven Jose mentions the bot"},
		{Kind: "batch", Ref: "37", At: at.Add(2 * time.Minute), Role: "context", Excerpt: "trace-eight batch 37 claude sonnet"},
	}
	mk := func(id int64, kind domain.FeedActionKind, prio domain.FeedPriority, rationale string, answers int64, excerpt string, opts ...domain.FeedOption) domain.FeedAction {
		tr := []domain.FeedTraceEntry{feedAnswers(answers, at, excerpt)}
		return domain.FeedAction{
			ID: id, BatchID: 37, Kind: kind, Priority: prio, Rationale: rationale, Answers: []int64{answers},
			Options: opts, Trace: tr, State: "proposed", CreatedAt: at, ExpiresAt: at.Add(24 * time.Hour),
		}
	}
	a12 := mk(12, domain.FeedSlackDraft, "high", "wtb-2628 owns that change and it is not merged.", 811,
		"Ana: does the settler config change break the bot?",
		feedOpt("Not yet", "Not yet, it is not merged."),
		feedOpt("No", "No, the bot reads only the threshold."),
		feedOpt("Which change?", "Which change do you mean?"))
	a12.Trace = trace
	a13 := mk(13, domain.FeedTodoAdd, "normal", "Review touches evm-settler like wtb-2628.", 812,
		"realfi-co/realfi#3441 asks for your review",
		domain.FeedOption{Label: "Add to-do", Params: json.RawMessage(`{"session":"wtb-2628","text":"review #3441"}`)},
		domain.FeedOption{Label: "Read it", Kind: domain.FeedRead, Params: json.RawMessage(`{"url":"https://github.com/realfi-co/realfi/pull/3441","why":"scope"}`)})
	a14 := mk(14, domain.FeedRead, "normal", "vault-fix is blocked on you.", 813,
		"vault-fix waits for your answer on the pause test",
		domain.FeedOption{Label: "Open", Params: json.RawMessage(`{"url":"aiman://vault-fix","why":"answer"}`)})
	a15 := mk(15, domain.FeedSlackDraft, "normal", "Jose asks for the threshold.", 814,
		"Jose: what is the redemption threshold now?",
		feedOpt("Tell him", "It is 5%."))
	ev := func(id int64, src domain.FeedSource, kind, title, channel string) domain.FeedEvent {
		return domain.FeedEvent{ID: id, Source: src, ExternalID: fmt.Sprint(id), At: at, Actor: "U1", ActorName: "Ana",
			Kind: kind, Title: title, Body: title, ThreadKey: "t" + fmt.Sprint(id), Class: "direct", Channel: channel}
	}
	return &feedFake{
		actions: []domain.FeedAction{a12, a13, a14, a15},
		events: map[int64]domain.FeedEvent{
			811: ev(811, "slack", "message", "Ana: does the settler config change break the bot?", "C1"),
			812: ev(812, "github", "review_requested", "realfi-co/realfi#3441 asks for your review", ""),
			813: ev(813, "aiman", "waiting_input", "vault-fix waits for your answer on the pause test", ""),
			814: ev(814, "slack", "message", "Jose: what is the redemption threshold now?", "D2"),
		},
	}
}

// feedModel is the sessions tab on one host, after the startup reads.
func feedModel(t *testing.T, f *feedFake) *Model {
	t.Helper()
	installFeedFake(t, f)
	cfg := &config.Config{Remotes: []config.Remote{{Name: "regent0", Host: feedHost, User: "code"}}}
	s := domain.Session{ID: "s1", Name: "wtb-2628", IssueKey: "WTB-2628", RemoteHost: feedHost, Status: domain.SessionStatusActive}
	m := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 50})
	runThroughUpdate(m, m.Init(), 0)
	if f.count("feed.actions") == 0 {
		t.Fatal("no feed.actions read at start")
	}
	return m
}

func feedView(m *Model) string { return ansi.Strip(m.View().Content) }

func feedKey(t *testing.T, m *Model, key string) *Model {
	t.Helper()
	next, cmd := m.Update(pressKey(key))
	m = next.(*Model)
	runThroughUpdate(m, cmd, 0)
	return m
}

func TestFeedHeaderCountsOpenAndHigh(t *testing.T) {
	m := feedModel(t, sampleFeed())
	if v := feedView(m); !strings.Contains(v, "feed 4 (1 high)") {
		t.Fatalf("header lacks %q:\n%s", "feed 4 (1 high)", v)
	}
}

func TestFeedPanelShowsTopThreeRows(t *testing.T) {
	m := feedModel(t, sampleFeed())
	v := feedView(m)
	var row string
	for _, l := range strings.Split(v, "\n") {
		if strings.Contains(l, "Ana: does the settler") {
			row = l
		}
	}
	if row == "" {
		t.Fatalf("panel lacks the high action:\n%s", v)
	}
	if !strings.Contains(row, "slack") || !strings.Contains(row, "3 answers") {
		t.Fatalf("row %q lacks its source or %q", row, "3 answers")
	}
	for _, want := range []string{"realfi#3441", "vault-fix waits", "+1 more"} {
		if !strings.Contains(v, want) {
			t.Errorf("panel lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "redemption threshold") {
		t.Errorf("collapsed panel shows a fourth row:\n%s", v)
	}
	if i, j := strings.Index(v, "wtb-2628"), strings.Index(v, "Ana: does the settler"); i < 0 || j < i {
		t.Errorf("panel is not under the session list:\n%s", v)
	}
}

func TestFeedPanelFocusShowsTheAction(t *testing.T) {
	f := sampleFeed()
	m := feedModel(t, f)
	if strings.Contains(feedView(m), "it is not merged.") {
		t.Fatal("rationale shown before the panel has focus")
	}
	reads := f.count("feed.actions")
	m = feedKey(t, m, "ctrl+f")
	v := feedView(m)
	for _, want := range []string{"wtb-2628 owns that change", "Not yet", "Which change"} {
		if !strings.Contains(v, want) {
			t.Errorf("focused detail lacks %q:\n%s", want, v)
		}
	}
	if f.count("feed.actions") <= reads {
		t.Error("focusing the panel did not read feed.actions")
	}
	m = feedKey(t, m, "esc")
	if strings.Contains(feedView(m), "wtb-2628 owns that change") {
		t.Error("esc left the action in the detail pane")
	}
}

// The number keys are the 1-based labels; feed.act's option is the 0-based
// index into options (spec §11: options[option]).
func TestFeedPanelNumberApprovesThatOption(t *testing.T) {
	f := sampleFeed()
	m := feedModel(t, f)
	m = feedKey(t, m, "ctrl+f")
	feedKey(t, m, "2")
	p := f.lastAct(t)
	if p["id"] != float64(12) || p["op"] != "approve" || p["option"] != float64(1) {
		t.Fatalf("feed.act %v, want id 12 approve option 1", p)
	}
	if _, ok := p["params"]; ok {
		t.Fatalf("an unedited approve sent params: %v", p)
	}
}

func TestFeedPanelDismissNotRelevant(t *testing.T) {
	f := sampleFeed()
	m := feedModel(t, f)
	m = feedKey(t, m, "ctrl+f")
	feedKey(t, m, "d")
	p := f.lastAct(t)
	if p["id"] != float64(12) || p["op"] != "dismiss" || p["reason"] != string(domain.FeedNotRelevant) {
		t.Fatalf("feed.act %v, want id 12 dismiss not_relevant", p)
	}
}

func TestFeedPanelDismissMenu(t *testing.T) {
	reasons := []string{"not relevant", "already handled", "later", "mute thread", "mute sender"}

	f := sampleFeed()
	m := feedModel(t, f)
	m = feedKey(t, m, "ctrl+f")
	m = feedKey(t, m, "D")
	v := feedView(m)
	for _, want := range reasons {
		if !strings.Contains(v, want) {
			t.Errorf("menu lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "mute channel") {
		t.Errorf("menu offers mute channel for an action that answers no digest:\n%s", v)
	}
	if f.count("feed.act") != 0 {
		t.Fatal("D sent feed.act before a reason was picked")
	}

	// An action answering a channel digest also offers mute channel.
	d := sampleFeed()
	d.events[811] = domain.FeedEvent{ID: 811, Source: "slack", ExternalID: "digest:C1:790-811", Kind: "channel_digest",
		Title: "#payments digest", Body: "Ana: does the settler config change break the bot?", ThreadKey: "slack:C1/790",
		Class: "ambient", Channel: "C1", At: time.Now()}
	m = feedModel(t, d)
	m = feedKey(t, m, "ctrl+f")
	m = feedKey(t, m, "D")
	if v := feedView(m); !strings.Contains(v, "mute channel") {
		t.Errorf("menu lacks mute channel for a digest action:\n%s", v)
	}
}

func TestFeedPanelTraceToggle(t *testing.T) {
	m := feedModel(t, sampleFeed())
	m = feedKey(t, m, "ctrl+f")
	before := feedView(m)
	m = feedKey(t, m, "t")
	shown := feedView(m)
	if shown == before || !strings.Contains(shown, "trace-eight") {
		t.Fatalf("t did not show the whole trace:\n%s", shown)
	}
	m = feedKey(t, m, "t")
	if after := feedView(m); after != before {
		t.Fatalf("second t did not restore the view:\n%s", after)
	}
}

// A feed hub event rereads the host's actions; nothing else polls.
func TestFeedHubEventRefetches(t *testing.T) {
	f := sampleFeed()
	m := feedModel(t, f)
	reads := f.count("feed.actions")
	if m.streams == nil {
		m.streams = map[string]*sessionStream{}
	}
	m.streams[feedHost] = &sessionStream{host: feedHost, events: make(chan server.SessionEvent)}

	// Settle: a pass of the program's own commands must not read again.
	runThroughUpdate(m, tea.Batch(tickPane(), tickGit()), 0)
	if f.count("feed.actions") != reads {
		t.Fatal("feed.actions read without a feed event or focus")
	}

	f.mu.Lock()
	f.actions = f.actions[1:]
	f.mu.Unlock()
	cmd := m.applySessionEvent(sessionEventMsg{host: feedHost, event: server.SessionEvent{Type: "feed", State: "done"}})
	runThroughUpdate(m, cmd, 0)
	if f.count("feed.actions") != reads+1 {
		t.Fatalf("feed.actions reads %d, want %d after the event", f.count("feed.actions"), reads+1)
	}
	if v := feedView(m); !strings.Contains(v, "feed 3") || strings.Contains(v, "(1 high)") {
		t.Fatalf("header not updated from the reread:\n%s", v)
	}
}

// The Feed settings page keeps feed.interval within 1m..60m.
func TestFeedSettingsInterval(t *testing.T) {
	var f config.FeedConfig
	for _, v := range []string{"1m", "10m", "60m"} {
		if err := applyFeedSetting(&f, "interval", v); err != nil || f.Interval != v {
			t.Fatalf("interval %q: err %v, stored %q", v, err, f.Interval)
		}
	}
	for _, v := range []string{"90m", "30s", "soon"} {
		if err := applyFeedSetting(&f, "interval", v); err == nil {
			t.Errorf("interval %q accepted", v)
		}
		if f.Interval != "60m" {
			t.Errorf("interval %q changed the stored value to %q", v, f.Interval)
		}
	}
}
