package feed_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/feed"
)

const (
	actDone      = domain.FeedActionState("done")
	actFailed    = domain.FeedActionState("failed")
	actDismissed = domain.FeedActionState("dismissed")
	actSnoozed   = domain.FeedActionState("snoozed")
)

// fakeMutes is feed.db's mute writer (feed.MuteWriter).
type fakeMutes struct{ mutes []domain.FeedMute }

func (f *fakeMutes) Mute(_ context.Context, m domain.FeedMute) error {
	f.mutes = append(f.mutes, m)
	return nil
}

type draftCall struct{ channel, threadTS, text string }

type actRig struct {
	store   *fakeStore
	events  *fakeEvents
	mutes   *fakeMutes
	now     time.Time
	drafts  []draftCall
	todos   [][2]string
	execErr error
	deps    feed.ActDeps
}

func newActRig() *actRig {
	r := &actRig{store: newFakeStore(), mutes: &fakeMutes{}, now: feedT0}
	r.events = &fakeEvents{events: []domain.FeedEvent{
		ev(1, func(e *domain.FeedEvent) { e.Actor, e.ThreadKey, e.Channel = "U1", "slack:C1/1", "C1" }),
		ev(2, func(e *domain.FeedEvent) {
			e.Source, e.Class, e.Actor, e.ThreadKey, e.Channel = "aiman", "session", "sess-1", "aiman:sess-1", ""
		}),
		ev(3, func(e *domain.FeedEvent) {
			e.Kind, e.Class, e.Channel, e.ExternalID, e.ThreadKey = "channel_digest", "ambient", "C9", "digest:C9:3-3", "slack:C9/3"
		}),
	}}
	r.deps = feed.ActDeps{
		Store:    r.store,
		Events:   r.events,
		Mutes:    r.mutes,
		MuteDays: 7,
		Now:      func() time.Time { return r.now },
		SlackDraft: func(_ context.Context, channel, threadTS, text string) (string, error) {
			r.drafts = append(r.drafts, draftCall{channel, threadTS, text})
			if r.execErr != nil {
				return "", r.execErr
			}
			return "https://slack.example/archives/C1/p1", nil
		},
		TodoAdd: func(_ context.Context, session, text string) error {
			r.todos = append(r.todos, [2]string{session, text})
			return r.execErr
		},
	}
	return r
}

// seed stores a proposed action answering answers, with a draft option and
// a to-do option.
func (r *actRig) seed(kind domain.FeedActionKind, answers ...int64) int64 {
	a, _ := r.store.InsertFeedAction(context.Background(), domain.FeedAction{
		BatchID: 1, Kind: kind, Rationale: "r", Priority: domain.FeedPriority("normal"), Answers: answers,
		Options: []domain.FeedOption{
			{Label: "Not yet", Params: json.RawMessage(`{"channel":"C1","text":"not yet"}`)},
			{Label: "Later", Kind: domain.FeedTodoAdd, Params: json.RawMessage(`{"session":"s1","text":"review it"}`)},
		},
		State: actProposed, CreatedAt: feedT0, ExpiresAt: feedT0.Add(24 * time.Hour),
	})
	return a.ID
}

func (r *actRig) stored(t *testing.T, id int64) domain.FeedAction {
	t.Helper()
	a, err := r.store.GetFeedAction(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func intp(n int) *int { return &n }

func TestActApprove(t *testing.T) {
	t.Run("edited draft", func(t *testing.T) {
		r := newActRig()
		id := r.seed(domain.FeedSlackDraft, 1)
		edited := json.RawMessage(`{"channel":"C1","thread_ts":"1.2","text":"edited answer"}`)
		if _, err := feed.Act(context.Background(), r.deps, id, feed.ActRequest{Op: "approve", Option: intp(0), Params: edited, By: "operator"}); err != nil {
			t.Fatal(err)
		}
		if len(r.drafts) != 1 || r.drafts[0] != (draftCall{"C1", "1.2", "edited answer"}) || len(r.todos) != 0 {
			t.Fatalf("drafts %+v, todos %+v; want one draft with the edited text", r.drafts, r.todos)
		}
		a := r.stored(t, id)
		if a.State != actDone || a.Chosen == nil || *a.Chosen != 0 || a.DecidedBy != "operator" || !a.DecidedAt.Equal(r.now) {
			t.Errorf("action = %+v", a)
		}
		if !strings.Contains(string(a.ChosenParams), "edited answer") {
			t.Errorf("ChosenParams = %s, want the edit", a.ChosenParams)
		}
		if !strings.Contains(a.Result, "https://slack.example/archives/C1/p1") {
			t.Errorf("Result = %q, want the draft's permalink", a.Result)
		}
	})
	t.Run("option with its own kind", func(t *testing.T) {
		r := newActRig()
		id := r.seed(domain.FeedSlackDraft, 1)
		if _, err := feed.Act(context.Background(), r.deps, id, feed.ActRequest{Op: "approve", Option: intp(1), By: "operator"}); err != nil {
			t.Fatal(err)
		}
		if len(r.todos) != 1 || r.todos[0] != [2]string{"s1", "review it"} || len(r.drafts) != 0 {
			t.Fatalf("todos %+v, drafts %+v; want option 1's to-do only", r.todos, r.drafts)
		}
		if a := r.stored(t, id); a.State != actDone || *a.Chosen != 1 {
			t.Errorf("action = %+v", a)
		}
	})
	t.Run("read", func(t *testing.T) {
		r := newActRig()
		a, _ := r.store.InsertFeedAction(context.Background(), domain.FeedAction{
			Kind: domain.FeedRead, Answers: []int64{1}, State: actProposed,
			Options: []domain.FeedOption{{Label: "open", Params: json.RawMessage(`{"url":"https://x","why":"w"}`)}},
		})
		if _, err := feed.Act(context.Background(), r.deps, a.ID, feed.ActRequest{Op: "approve", Option: intp(0)}); err != nil {
			t.Fatal(err)
		}
		if got := r.stored(t, a.ID); got.State != actDone || len(r.drafts)+len(r.todos) != 0 {
			t.Errorf("state %s, executors %d; want done with no executor", got.State, len(r.drafts)+len(r.todos))
		}
	})
	t.Run("executor fails", func(t *testing.T) {
		r := newActRig()
		r.execErr = errors.New("slack said boom")
		id := r.seed(domain.FeedSlackDraft, 1)
		_, _ = feed.Act(context.Background(), r.deps, id, feed.ActRequest{Op: "approve", Option: intp(0)})
		if a := r.stored(t, id); a.State != actFailed || !strings.Contains(a.Result, "boom") {
			t.Errorf("action = %s %q, want failed with the error", a.State, a.Result)
		}
	})
	t.Run("option out of range", func(t *testing.T) {
		r := newActRig()
		id := r.seed(domain.FeedSlackDraft, 1)
		if _, err := feed.Act(context.Background(), r.deps, id, feed.ActRequest{Op: "approve", Option: intp(4)}); err == nil {
			t.Error("approve option 4 succeeded")
		}
		if a := r.stored(t, id); a.State != actProposed || len(r.drafts)+len(r.todos) != 0 {
			t.Errorf("action %s, executors %d; want it untouched", a.State, len(r.drafts)+len(r.todos))
		}
	})
}

func TestActDismiss(t *testing.T) {
	muteUntil := feedT0.Add(7 * 24 * time.Hour)
	cases := []struct {
		name    string
		answers []int64
		reason  domain.FeedDismissReason
		state   domain.FeedActionState
		mutes   []domain.FeedMute // each must be written; no other
		holds   []string
		refused bool
	}{
		{name: "default", answers: []int64{1}, state: actDismissed},
		{name: "not relevant", answers: []int64{1}, reason: domain.FeedNotRelevant, state: actDismissed},
		{name: "handled", answers: []int64{1}, reason: domain.FeedHandled, state: actDismissed, holds: []string{"slack:C1/1"}},
		{name: "mute thread", answers: []int64{1}, reason: domain.FeedMuteThread, state: actDismissed,
			mutes: []domain.FeedMute{{Scope: "thread", Key: "slack:C1/1", Until: muteUntil}}},
		{name: "mute sender skips sessions", answers: []int64{1, 2}, reason: domain.FeedMuteSender, state: actDismissed,
			mutes: []domain.FeedMute{{Scope: "sender", Key: "U1", Until: muteUntil}}},
		{name: "mute channel of a digest", answers: []int64{3}, reason: domain.FeedMuteChannel, state: actDismissed,
			mutes: []domain.FeedMute{{Scope: "channel", Key: "C9", Until: muteUntil}}},
		{name: "mute channel needs a digest", answers: []int64{1}, reason: domain.FeedMuteChannel, state: actProposed, refused: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newActRig()
			id := r.seed(domain.FeedSlackDraft, c.answers...)
			_, err := feed.Act(context.Background(), r.deps, id, feed.ActRequest{Op: "dismiss", Reason: c.reason, Note: "n1", By: "operator"})
			if (err != nil) != c.refused {
				t.Fatalf("err = %v, refused %v", err, c.refused)
			}
			a := r.stored(t, id)
			if a.State != c.state {
				t.Errorf("state = %s, want %s", a.State, c.state)
			}
			if !c.refused {
				want := c.reason
				if want == "" {
					want = domain.FeedNotRelevant
				}
				if a.Dismissal == nil || a.Dismissal.Reason != want || a.Dismissal.Note != "n1" {
					t.Errorf("Dismissal = %+v, want %s with the note", a.Dismissal, want)
				}
			}
			if len(r.mutes.mutes) != len(c.mutes) {
				t.Fatalf("mutes = %+v, want %+v", r.mutes.mutes, c.mutes)
			}
			for i, m := range c.mutes {
				got := r.mutes.mutes[i]
				if got.Scope != m.Scope || got.Key != m.Key || !got.Until.Equal(m.Until) || got.ActionID != id {
					t.Errorf("mute %d = %+v, want %+v for action %d", i, got, m, id)
				}
			}
			for _, key := range c.holds {
				if r.store.holds[key] < 1 {
					t.Errorf("thread %s not held through event 1: %v", key, r.store.holds)
				}
			}
			if len(c.holds) == 0 && len(r.store.holds) != 0 {
				t.Errorf("holds = %v, want none", r.store.holds)
			}
			if len(r.drafts)+len(r.todos) != 0 {
				t.Error("a dismissal ran an executor")
			}
		})
	}
}

func TestActLaterSnoozesThenComesBack(t *testing.T) {
	r := newActRig()
	id := r.seed(domain.FeedSlackDraft, 1)
	until := feedT0.Add(time.Hour)
	if _, err := feed.Act(context.Background(), r.deps, id, feed.ActRequest{Op: "dismiss", Reason: domain.FeedLater, Until: until}); err != nil {
		t.Fatal(err)
	}
	a := r.stored(t, id)
	if a.State != actSnoozed || a.Dismissal == nil || !a.Dismissal.Until.Equal(until) {
		t.Fatalf("action = %s %+v, want snoozed until %s", a.State, a.Dismissal, until)
	}
	r.now = until.Add(-time.Second)
	if _, err := feed.WakeSnoozed(context.Background(), r.deps); err != nil {
		t.Fatal(err)
	}
	if s := r.stored(t, id).State; s != actSnoozed {
		t.Errorf("state before the time = %s, want snoozed", s)
	}
	r.now = until
	n, err := feed.WakeSnoozed(context.Background(), r.deps)
	if err != nil {
		t.Fatal(err)
	}
	if s := r.stored(t, id).State; s != actProposed || n != 1 {
		t.Errorf("state at the time = %s (woke %d), want proposed", s, n)
	}
}

func TestActApproveDismissedRefused(t *testing.T) {
	r := newActRig()
	id := r.seed(domain.FeedSlackDraft, 1)
	if _, err := feed.Act(context.Background(), r.deps, id, feed.ActRequest{Op: "dismiss"}); err != nil {
		t.Fatal(err)
	}
	if _, err := feed.Act(context.Background(), r.deps, id, feed.ActRequest{Op: "approve", Option: intp(0)}); err == nil {
		t.Error("approve on a dismissed action succeeded")
	}
	if a := r.stored(t, id); a.State != actDismissed || len(r.drafts)+len(r.todos) != 0 {
		t.Errorf("state %s, executors %d; want dismissed and nothing run", a.State, len(r.drafts)+len(r.todos))
	}
}
