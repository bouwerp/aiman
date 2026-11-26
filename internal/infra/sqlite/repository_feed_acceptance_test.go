//go:build feedaccept

package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

var feedRepoT0 = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

func feedRepo(t *testing.T) *Repository {
	t.Helper()
	r, err := NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func feedAction(kind domain.FeedActionKind, prio domain.FeedPriority, state domain.FeedActionState, at time.Time, answers ...int64) domain.FeedAction {
	return domain.FeedAction{
		BatchID: 1, Kind: kind, Rationale: "r", Priority: prio, Answers: answers,
		Options:   []domain.FeedOption{{Label: "ok", Params: json.RawMessage(`{"url":"https://x"}`)}},
		State:     state,
		CreatedAt: at, ExpiresAt: at.Add(24 * time.Hour),
	}
}

func insertAction(t *testing.T, r *Repository, a domain.FeedAction) domain.FeedAction {
	t.Helper()
	got, err := r.InsertFeedAction(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == 0 {
		t.Fatal("inserted action has no id")
	}
	return got
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFeedCutBatchSetsHighWater(t *testing.T) {
	r := feedRepo(t)
	ctx := context.Background()
	hw, err := r.FeedHighWater(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hw != 0 {
		t.Fatalf("high water before any batch = %d, want 0", hw)
	}

	b, err := r.CutFeedBatch(ctx, domain.FeedBatch{EventIDs: []int64{3, 5, 9}, UpToEventID: 9, State: "running", StartedAt: feedRepoT0})
	if err != nil {
		t.Fatal(err)
	}
	if b.ID == 0 {
		t.Fatal("cut batch has no id")
	}
	if hw, _ = r.FeedHighWater(ctx); hw != 9 {
		t.Fatalf("high water = %d, want 9", hw)
	}
	got, err := r.GetFeedBatch(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.EventIDs, []int64{3, 5, 9}) || got.UpToEventID != 9 || got.State != "running" || !got.StartedAt.Equal(feedRepoT0) {
		t.Fatalf("batch = %+v", got)
	}

	b2, err := r.CutFeedBatch(ctx, domain.FeedBatch{EventIDs: []int64{12}, UpToEventID: 12, State: "running", StartedAt: feedRepoT0.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if hw, _ = r.FeedHighWater(ctx); hw != 12 {
		t.Fatalf("high water = %d, want 12", hw)
	}

	running, err := r.RunningFeedBatches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 2 {
		t.Fatalf("running = %+v", running)
	}
	b.State, b.Summary, b.Attempts, b.EndedAt = "done", "two drafts", 1, feedRepoT0.Add(30*time.Second)
	if err := r.UpdateFeedBatch(ctx, b); err != nil {
		t.Fatal(err)
	}
	if running, _ = r.RunningFeedBatches(ctx); len(running) != 1 || running[0].ID != b2.ID {
		t.Fatalf("running after done = %+v", running)
	}
	list, err := r.ListFeedBatches(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != b2.ID || list[1].Summary != "two drafts" || list[1].Attempts != 1 {
		t.Fatalf("batches newest first = %+v", list)
	}
	if n, err := r.DailyFeedBatches(ctx, feedRepoT0.Add(-time.Hour)); err != nil || n != 2 {
		t.Fatalf("daily batches = %d, %v; want 2", n, err)
	}
}

func TestFeedListActionsHighPriorityFirst(t *testing.T) {
	r := feedRepo(t)
	ctx := context.Background()
	// Inserted oldest first, so id order and time order agree.
	e := insertAction(t, r, feedAction(domain.FeedRead, "low", "proposed", feedRepoT0.Add(-2*time.Hour), 1))
	d := insertAction(t, r, feedAction(domain.FeedRead, "high", "proposed", feedRepoT0.Add(-time.Hour), 2))
	a := insertAction(t, r, feedAction(domain.FeedRead, "normal", "proposed", feedRepoT0.Add(time.Minute), 3))
	b := insertAction(t, r, feedAction(domain.FeedRead, "high", "proposed", feedRepoT0.Add(2*time.Minute), 4))
	c := insertAction(t, r, feedAction(domain.FeedRead, "normal", "proposed", feedRepoT0.Add(3*time.Minute), 5))
	insertAction(t, r, feedAction(domain.FeedRead, "high", "dismissed", feedRepoT0.Add(4*time.Minute), 6))

	got, err := r.ListFeedActions(ctx, []domain.FeedActionState{"proposed"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{b.ID, d.ID, c.ID, a.ID, e.ID}
	gotIDs := make([]int64, len(got))
	for i, x := range got {
		gotIDs[i] = x.ID
	}
	if !slices.Equal(gotIDs, want) {
		t.Fatalf("order = %v, want %v (high first, then newest)", gotIDs, want)
	}
	if limited, _ := r.ListFeedActions(ctx, []domain.FeedActionState{"proposed"}, 2); len(limited) != 2 || limited[0].ID != b.ID {
		t.Fatalf("limit 2 = %+v", limited)
	}
	both, err := r.ListFeedActions(ctx, []domain.FeedActionState{"proposed", "dismissed"}, 10)
	if err != nil || len(both) != 6 {
		t.Fatalf("two states = %d, %v; want 6", len(both), err)
	}
}

func TestFeedOpenActionEventFloor(t *testing.T) {
	r := feedRepo(t)
	ctx := context.Background()
	if floor, err := r.OpenFeedActionEventFloor(ctx); err != nil || floor != 0 {
		t.Fatalf("floor with no actions = %d, %v; want 0", floor, err)
	}

	open := feedAction(domain.FeedSlackDraft, "high", "proposed", feedRepoT0, 50)
	open.Trace = []domain.FeedTraceEntry{
		{Kind: "event", Ref: "40", Role: "cause", At: feedRepoT0},
		{Kind: "memory", Ref: "m1", Role: "context", At: feedRepoT0},
		{Kind: "decision", Ref: "2", Role: "context", At: feedRepoT0}, // an action id, not an event
		{Kind: "batch", Ref: "1", Role: "context", At: feedRepoT0},
	}
	insertAction(t, r, open)
	insertAction(t, r, feedAction(domain.FeedRead, "normal", "snoozed", feedRepoT0, 30))
	for _, closed := range []domain.FeedActionState{"done", "failed", "dismissed", "expired"} {
		insertAction(t, r, feedAction(domain.FeedRead, "normal", closed, feedRepoT0, 10))
	}

	floor, err := r.OpenFeedActionEventFloor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if floor != 30 {
		t.Fatalf("floor = %d, want 30 (snoozed answers; closed actions and non-event refs ignored)", floor)
	}
}

func TestFeedThreadHold(t *testing.T) {
	r := feedRepo(t)
	ctx := context.Background()
	if err := r.HoldFeedThread(ctx, "slack:C1/1.0", 100); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		thread string
		event  int64
		held   bool
	}{
		{"slack:C1/1.0", 99, true},
		{"slack:C1/1.0", 100, true},
		{"slack:C1/1.0", 101, false},
		{"slack:C2/1.0", 50, false},
	} {
		held, err := r.FeedThreadHeld(ctx, tc.thread, tc.event)
		if err != nil {
			t.Fatal(err)
		}
		if held != tc.held {
			t.Errorf("FeedThreadHeld(%s, %d) = %v, want %v", tc.thread, tc.event, held, tc.held)
		}
	}
	if err := r.HoldFeedThread(ctx, "slack:C1/1.0", 120); err != nil {
		t.Fatal(err)
	}
	if held, _ := r.FeedThreadHeld(ctx, "slack:C1/1.0", 110); !held {
		t.Fatal("a second hold did not move the thread's limit to 120")
	}
}

func TestFeedActionJSONRoundTrip(t *testing.T) {
	r := feedRepo(t)
	ctx := context.Background()
	in := domain.FeedAction{
		BatchID: 7, Kind: domain.FeedSlackDraft, Rationale: "Ana asked", Priority: "high",
		Answers: []int64{811, 812},
		Options: []domain.FeedOption{
			{Label: "Not yet", Params: json.RawMessage(`{"channel":"C1","thread_ts":"1.0","text":"Not yet."}`), Why: "branch open"},
			{Label: "Add a to-do", Kind: domain.FeedTodoAdd, Params: json.RawMessage(`{"session":"wtb","text":"review"}`)},
		},
		Trace: []domain.FeedTraceEntry{
			{Kind: "event", Ref: "790", At: feedRepoT0, Role: "cause", Excerpt: "is it moving?", URL: "https://slack/x"},
			{Kind: "memory", Ref: "m12", At: feedRepoT0.Add(-time.Hour), Role: "context", Excerpt: "Ana asks about settler"},
		},
		State: "proposed", CreatedAt: feedRepoT0, ExpiresAt: feedRepoT0.Add(24 * time.Hour),
	}
	stored := insertAction(t, r, in)
	got, err := r.GetFeedAction(ctx, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BatchID != 7 || got.Kind != in.Kind || got.Rationale != in.Rationale || got.Priority != "high" || got.State != "proposed" ||
		!slices.Equal(got.Answers, in.Answers) || !got.CreatedAt.Equal(in.CreatedAt) || !got.ExpiresAt.Equal(in.ExpiresAt) {
		t.Fatalf("action = %+v", got)
	}
	if mustJSON(t, got.Options) != mustJSON(t, in.Options) {
		t.Fatalf("options = %s, want %s", mustJSON(t, got.Options), mustJSON(t, in.Options))
	}
	if mustJSON(t, got.Trace) != mustJSON(t, in.Trace) {
		t.Fatalf("trace = %s, want %s", mustJSON(t, got.Trace), mustJSON(t, in.Trace))
	}
	if got.Dismissal != nil || got.Chosen != nil {
		t.Fatalf("new action has dismissal %+v, chosen %v", got.Dismissal, got.Chosen)
	}

	chosen := 1
	got.State, got.Chosen, got.ChosenParams = "done", &chosen, json.RawMessage(`{"session":"wtb","text":"review today"}`)
	got.Result, got.DecidedBy, got.DecidedAt = "added", "operator", feedRepoT0.Add(time.Minute)
	if err := r.UpdateFeedAction(ctx, got); err != nil {
		t.Fatal(err)
	}
	done, err := r.GetFeedAction(ctx, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.State != "done" || done.Chosen == nil || *done.Chosen != 1 || string(done.ChosenParams) != `{"session":"wtb","text":"review today"}` ||
		done.Result != "added" || done.DecidedBy != "operator" || !done.DecidedAt.Equal(feedRepoT0.Add(time.Minute)) {
		t.Fatalf("decided action = %+v", done)
	}

	other := insertAction(t, r, feedAction(domain.FeedRead, "normal", "proposed", feedRepoT0, 900))
	other.State = "snoozed"
	other.Dismissal = &domain.FeedDismissal{Reason: domain.FeedLater, Note: "after lunch", Until: feedRepoT0.Add(3 * time.Hour)}
	if err := r.UpdateFeedAction(ctx, other); err != nil {
		t.Fatal(err)
	}
	snoozed, err := r.GetFeedAction(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snoozed.Dismissal == nil || snoozed.Dismissal.Reason != domain.FeedLater || snoozed.Dismissal.Note != "after lunch" ||
		!snoozed.Dismissal.Until.Equal(feedRepoT0.Add(3*time.Hour)) {
		t.Fatalf("dismissal = %+v", snoozed.Dismissal)
	}
}
