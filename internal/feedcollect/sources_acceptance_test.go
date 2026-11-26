//go:build feedaccept

package feedcollect_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/feedcollect"
)

// Acceptance tests for the aiman and GitHub sources (P3a).

// ---- aiman ----

type fakeNotif struct {
	Seq       int64  `json:"seq"`
	SessionID string `json:"session_id"`
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Host      string `json:"host"`
	Text      string `json:"text"`
	At        string `json:"at"`
}

type fakeLedgerRow struct {
	RowID     int64           `json:"rowid"`
	SessionID string          `json:"session_id"`
	Seq       int64           `json:"seq"`
	At        time.Time       `json:"at"`
	Actor     string          `json:"actor"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
}

type fakeSession struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ParentID string `json:"parent_id,omitempty"`
	Role     string `json:"role,omitempty"`
}

// fakeServe answers notify.since, session.events_since and session.list the
// way serve does. after < 0 (notify) and after_rowid < 0 (ledger) answer
// nothing and the current tail.
type fakeServe struct {
	mu       sync.Mutex
	notifs   []fakeNotif
	ledger   []fakeLedgerRow
	sessions []fakeSession
	down     bool
	calls    []string
	params   []map[string]any
}

func (f *fakeServe) rpc(_ context.Context, method string, params any, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var p map[string]any
	if params != nil {
		b, _ := json.Marshal(params)
		_ = json.Unmarshal(b, &p)
	}
	f.calls = append(f.calls, method)
	f.params = append(f.params, p)
	if f.down {
		return errors.New("dial unix aiman.sock: connection refused")
	}
	var res any
	switch method {
	case "notify.since":
		after := int64(num(p["after"]))
		last := int64(0)
		if n := len(f.notifs); n > 0 {
			last = f.notifs[n-1].Seq
		}
		entries := []fakeNotif{}
		if after >= 0 {
			for _, n := range f.notifs {
				if n.Seq > after {
					entries = append(entries, n)
				}
			}
		}
		res = map[string]any{"type": "notifications", "entries": entries, "last_seq": last}
	case "session.events_since":
		after := int64(num(p["after_rowid"]))
		var kinds []string
		if ks, ok := p["kinds"].([]any); ok {
			for _, k := range ks {
				kinds = append(kinds, fmt.Sprint(k))
			}
		}
		next := after
		rows := []fakeLedgerRow{}
		if after < 0 {
			if n := len(f.ledger); n > 0 {
				next = f.ledger[n-1].RowID
			} else {
				next = 0
			}
		} else {
			for _, r := range f.ledger {
				if r.RowID <= after {
					continue
				}
				next = r.RowID
				if len(kinds) == 0 || slices.Contains(kinds, r.Kind) {
					rows = append(rows, r)
				}
			}
		}
		res = map[string]any{"type": "session_events_since", "events": rows, "next": next}
	case "session.list":
		res = map[string]any{"type": "session_list", "sessions": f.sessions}
	default:
		return fmt.Errorf("unknown method %s", method)
	}
	b, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

func (f *fakeServe) paramsOf(method string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for i, m := range f.calls {
		if m == method {
			out = append(out, f.params[i])
		}
	}
	return out
}

func notif(seq int64, session, kind string) fakeNotif {
	return fakeNotif{Seq: seq, SessionID: session, Kind: kind, Label: "s-" + session, Host: "h", Text: kind + " on " + session,
		At: collectT0.Add(time.Duration(seq) * time.Second).Format(time.RFC3339)}
}

func ledgerRow(rowid int64, session, kind string) fakeLedgerRow {
	return fakeLedgerRow{RowID: rowid, SessionID: session, Seq: rowid, At: collectT0.Add(time.Duration(rowid) * time.Second),
		Actor: "aiman", Kind: kind, Payload: json.RawMessage(`{"child":"C"}`)}
}

// newServe is a host with root session P, its child C and a hidden consult H,
// and history that predates the collector.
func newServe() *fakeServe {
	return &fakeServe{
		sessions: []fakeSession{{ID: "P", Name: "parent"}, {ID: "C", Name: "child", ParentID: "P"}, {ID: "H", Name: "consult-1", Role: "consult"}},
		notifs:   []fakeNotif{notif(10, "P", "waiting_input")},
		ledger:   []fakeLedgerRow{ledgerRow(100, "P", "child.landed")},
	}
}

func aimanPoll(t *testing.T, src feedcollect.Source, cursors map[string]string, mutes []domain.FeedMute) ([]domain.FeedEvent, map[string]string) {
	t.Helper()
	evs, next, err := src.Poll(context.Background(), cursors, mutes)
	if err != nil {
		t.Fatal(err)
	}
	return evs, next
}

func externalIDs(evs []domain.FeedEvent) []string {
	out := make([]string, len(evs))
	for i, ev := range evs {
		out[i] = ev.ExternalID
	}
	slices.Sort(out)
	return out
}

func TestAimanSourceFirstStartReadsFromTail(t *testing.T) {
	serve := newServe()
	src := feedcollect.NewAimanSource(serve.rpc, true)
	if src.Name() != "aiman" {
		t.Fatalf("name = %q", src.Name())
	}
	evs, cur := aimanPoll(t, src, map[string]string{}, nil)
	if len(evs) != 0 {
		t.Fatalf("first start replayed history: %+v", evs)
	}
	if len(cur) == 0 {
		t.Fatal("first start set no cursors")
	}
	// Nothing new: the next poll asks after the tail and finds nothing.
	evs, _ = aimanPoll(t, src, cur, nil)
	if len(evs) != 0 {
		t.Fatalf("second poll replayed history: %+v", evs)
	}
	for _, p := range serve.paramsOf("notify.since")[1:] {
		if num(p["after"]) != 10 {
			t.Fatalf("notify.since after = %v, want the tail 10", p["after"])
		}
	}
	for _, p := range serve.paramsOf("session.events_since")[1:] {
		if num(p["after_rowid"]) != 100 {
			t.Fatalf("session.events_since after_rowid = %v, want the tail 100", p["after_rowid"])
		}
	}
}

func TestAimanSourceKindsRootsAndCursors(t *testing.T) {
	serve := newServe()
	src := feedcollect.NewAimanSource(serve.rpc, true)
	_, cur := aimanPoll(t, src, map[string]string{}, nil)

	serve.notifs = append(serve.notifs,
		notif(11, "C", "waiting_input"), // child: dropped
		notif(12, "P", "waiting_input"),
		notif(13, "", "agent_update"), // kind not read
		notif(14, "P", "ended"),
		notif(15, "H", "error"), // hidden: dropped
		notif(16, "P", "ci_escalation"),
		notif(17, "P", "review_done"),
		notif(18, "P", "error"),
	)
	serve.ledger = append(serve.ledger,
		ledgerRow(101, "P", "child.landed"),
		ledgerRow(102, "P", "session.created"),  // kind not read
		ledgerRow(103, "C", "consult.finished"), // child's own ledger: dropped
		ledgerRow(104, "P", "design.published"),
		ledgerRow(105, "P", "child.finished"),
		ledgerRow(106, "P", "consult.finished"),
		ledgerRow(107, "P", "review.posted"),
		ledgerRow(108, "P", "harness.verdict"),
	)
	evs, cur2 := aimanPoll(t, src, cur, nil)
	want := []string{"ledger:101", "ledger:104", "ledger:105", "ledger:106", "ledger:107", "ledger:108",
		"notify:12", "notify:14", "notify:16", "notify:17", "notify:18"}
	if got := externalIDs(evs); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for _, ev := range evs {
		if ev.Source != "aiman" || ev.Class != "session" || ev.ThreadKey != "aiman:P" || ev.Actor != "P" || ev.Kind == "" || ev.At.IsZero() {
			t.Errorf("event %s = %+v", ev.ExternalID, ev)
		}
		if urgent := ev.Kind == "waiting_input"; ev.Urgent != urgent {
			t.Errorf("event %s kind %s urgent = %v", ev.ExternalID, ev.Kind, ev.Urgent)
		}
	}

	// The cursors moved to notify seq 18 and ledger rowid 108.
	aimanPoll(t, src, cur2, nil)
	ns := serve.paramsOf("notify.since")
	ls := serve.paramsOf("session.events_since")
	if num(ns[len(ns)-1]["after"]) != 18 || num(ls[len(ls)-1]["after_rowid"]) != 108 {
		t.Fatalf("last cursors asked: notify after %v, ledger after_rowid %v", ns[len(ns)-1]["after"], ls[len(ls)-1]["after_rowid"])
	}
}

func TestAimanSourceAllSessionsWhenNotRootOnly(t *testing.T) {
	serve := newServe()
	src := feedcollect.NewAimanSource(serve.rpc, false)
	_, cur := aimanPoll(t, src, map[string]string{}, nil)
	serve.notifs = append(serve.notifs, notif(11, "C", "waiting_input"))
	evs, _ := aimanPoll(t, src, cur, nil)
	if got := externalIDs(evs); !slices.Equal(got, []string{"notify:11"}) {
		t.Fatalf("events = %v, want the child's notification", got)
	}
}

func TestAimanSourceServeDownFails(t *testing.T) {
	serve := newServe()
	src := feedcollect.NewAimanSource(serve.rpc, true)
	_, cur := aimanPoll(t, src, map[string]string{}, nil)
	serve.down = true
	if _, _, err := src.Poll(context.Background(), cur, nil); err == nil {
		t.Fatal("Poll with serve down succeeded, so the collector would move the cursors")
	}
}

func TestAimanSourceRespectsThreadMute(t *testing.T) {
	serve := newServe()
	serve.sessions = append(serve.sessions, fakeSession{ID: "Q", Name: "other"})
	src := feedcollect.NewAimanSource(serve.rpc, true)
	_, cur := aimanPoll(t, src, map[string]string{}, nil)
	serve.notifs = append(serve.notifs, notif(11, "P", "ended"), notif(12, "Q", "ended"))
	mutes := []domain.FeedMute{{Scope: "thread", Key: "aiman:P", Until: collectT0.Add(time.Hour)}}
	evs, _ := aimanPoll(t, src, cur, mutes)
	if got := externalIDs(evs); !slices.Equal(got, []string{"notify:12"}) {
		t.Fatalf("events = %v, want only Q's", got)
	}
}

// ---- github ----

// fakeGH answers `gh api` for notifications. With --include (or -i) it
// prints the status line and headers first, as gh does; a request carrying
// If-None-Match with the current ETag gets 304, which gh prints and then
// exits 1, as it does for any status above 299.
type fakeGH struct {
	mu    sync.Mutex
	etag  string
	body  string
	calls [][]string
}

func (f *fakeGH) run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string(nil), args...))
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "notifications") {
		return nil, fmt.Errorf("unexpected gh call %q", joined)
	}
	include := slices.Contains(args, "-i") || slices.Contains(args, "--include")
	if strings.Contains(joined, "If-None-Match: "+f.etag) {
		if !include {
			return nil, errors.New("gh: HTTP 304")
		}
		return []byte("HTTP/2.0 304 Not Modified\r\nEtag: " + f.etag + "\r\n\r\n"), errors.New("exit status 1")
	}
	if !include {
		return []byte(f.body), nil
	}
	return []byte("HTTP/2.0 200 OK\r\nContent-Type: application/json; charset=utf-8\r\nEtag: " + f.etag + "\r\n\r\n" + f.body), nil
}

func (f *fakeGH) lastArgs() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls[len(f.calls)-1], " ")
}

func ghNotification(id, reason, subjectType, url, updated string) map[string]any {
	return map[string]any{
		"id": id, "unread": true, "reason": reason, "updated_at": updated,
		"subject":    map[string]any{"title": "Title " + id, "url": url, "type": subjectType},
		"repository": map[string]any{"full_name": "acme/widgets", "html_url": "https://github.com/acme/widgets"},
	}
}

func ghBody(t *testing.T, ns ...map[string]any) string {
	t.Helper()
	if ns == nil {
		ns = []map[string]any{}
	}
	b, err := json.Marshal(ns)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func byExternal(evs []domain.FeedEvent) map[string]domain.FeedEvent {
	out := map[string]domain.FeedEvent{}
	for _, ev := range evs {
		out[ev.ExternalID] = ev
	}
	return out
}

func TestGitHubSourceThreadsAndUrgency(t *testing.T) {
	gh := &fakeGH{etag: `W/"e1"`, body: ghBody(t,
		ghNotification("901", "review_requested", "PullRequest", "https://api.github.com/repos/acme/widgets/pulls/42", "2026-11-20T09:00:00Z"),
		ghNotification("902", "mention", "Issue", "https://api.github.com/repos/acme/widgets/issues/7", "2026-11-20T09:01:00Z"),
		ghNotification("903", "comment", "PullRequest", "https://api.github.com/repos/acme/widgets/pulls/43", "2026-11-20T09:02:00Z"),
		ghNotification("904", "assign", "Release", "", "2026-11-20T09:03:00Z"),
	)}
	src := feedcollect.NewGitHubSource(gh.run)
	if src.Name() != "github" {
		t.Fatalf("name = %q", src.Name())
	}
	evs, cur, err := src.Poll(context.Background(), map[string]string{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// ExternalID is "<notification id>@<updated_at>", so a later update to the
	// same notification thread is a new event, not a duplicate.
	got := map[string]domain.FeedEvent{}
	for _, ev := range evs {
		id, updated, ok := strings.Cut(ev.ExternalID, "@")
		if !ok || updated == "" {
			t.Errorf("external id %q, want <id>@<updated_at>", ev.ExternalID)
		}
		got[id] = ev
	}
	cases := []struct {
		id, thread, kind string
		urgent           bool
	}{
		{"901", "gh:acme/widgets#42", "review_requested", true},
		{"902", "gh:acme/widgets#7", "mention", true},
		{"903", "gh:acme/widgets#43", "comment", false},
		{"904", "gh:904", "assign", false},
	}
	for _, c := range cases {
		ev, ok := got[c.id]
		if !ok {
			t.Errorf("notification %s missing from %v", c.id, externalIDs(evs))
			continue
		}
		if ev.Source != "github" || ev.ThreadKey != c.thread || ev.Kind != c.kind || ev.Urgent != c.urgent || ev.Title == "" || ev.At.IsZero() {
			t.Errorf("notification %s = %+v, want thread %s kind %s urgent %v", c.id, ev, c.thread, c.kind, c.urgent)
		}
	}
	if u := got["901"].URL; u != "https://github.com/acme/widgets/pull/42" {
		t.Errorf("PR url = %q", u)
	}
	if len(cur) == 0 {
		t.Fatal("no cursors after a poll")
	}
}

func TestGitHubSourceSinceAndETag(t *testing.T) {
	gh := &fakeGH{etag: `W/"e1"`, body: ghBody(t,
		ghNotification("901", "mention", "Issue", "https://api.github.com/repos/acme/widgets/issues/7", "2026-11-20T09:00:00Z"))}
	src := feedcollect.NewGitHubSource(gh.run)
	ctx := context.Background()
	_, cur, err := src.Poll(ctx, map[string]string{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Unchanged: the poll sends the ETag and since, gets 304, and returns
	// nothing with the cursors it was given.
	evs, cur2, err := src.Poll(ctx, cur, nil)
	if err != nil {
		t.Fatalf("a 304 answer is not an error: %v", err)
	}
	args := gh.lastArgs()
	if !strings.Contains(args, `If-None-Match: W/"e1"`) || !strings.Contains(args, "since=") {
		t.Fatalf("second call %q lacks If-None-Match or since", args)
	}
	if len(evs) != 0 {
		t.Fatalf("304 returned events: %+v", evs)
	}
	if len(cur2) != len(cur) {
		t.Fatalf("304 changed the cursors: %v -> %v", cur, cur2)
	}
	for k, v := range cur {
		if cur2[k] != v {
			t.Fatalf("304 changed cursor %s: %q -> %q", k, v, cur2[k])
		}
	}

	// Changed: a new ETag is answered and kept.
	gh.etag = `W/"e2"`
	gh.body = ghBody(t, ghNotification("905", "review_requested", "PullRequest", "https://api.github.com/repos/acme/widgets/pulls/50", "2026-11-20T10:00:00Z"))
	evs, cur3, err := src.Poll(ctx, cur2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := externalIDs(evs); !slices.Equal(got, []string{"905"}) {
		t.Fatalf("events = %v", got)
	}
	if _, _, err := src.Poll(ctx, cur3, nil); err != nil {
		t.Fatal(err)
	}
	if args := gh.lastArgs(); !strings.Contains(args, `If-None-Match: W/"e2"`) {
		t.Fatalf("call after a change %q does not send the new ETag", args)
	}
}

func TestGitHubSourceRespectsThreadMute(t *testing.T) {
	gh := &fakeGH{etag: `W/"e1"`, body: ghBody(t,
		ghNotification("901", "mention", "PullRequest", "https://api.github.com/repos/acme/widgets/pulls/42", "2026-11-20T09:00:00Z"),
		ghNotification("902", "mention", "PullRequest", "https://api.github.com/repos/acme/widgets/pulls/43", "2026-11-20T09:00:00Z"),
	)}
	src := feedcollect.NewGitHubSource(gh.run)
	mutes := []domain.FeedMute{{Scope: "thread", Key: "gh:acme/widgets#42", Until: collectT0.Add(time.Hour)}}
	evs, _, err := src.Poll(context.Background(), map[string]string{}, mutes)
	if err != nil {
		t.Fatal(err)
	}
	if got := externalIDs(evs); !slices.Equal(got, []string{"902"}) {
		t.Fatalf("events = %v, want the muted thread left out", got)
	}
}

func TestGitHubSourceFailureIsAnError(t *testing.T) {
	src := feedcollect.NewGitHubSource(func(context.Context, ...string) ([]byte, error) {
		return nil, errors.New("gh: HTTP 401: Bad credentials")
	})
	if _, _, err := src.Poll(context.Background(), map[string]string{}, nil); err == nil {
		t.Fatal("a failed gh call was not an error")
	}
}
