//go:build feedaccept

package feed_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/agentjob"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/feed"
)

const (
	batchRunning = domain.FeedBatchState("running")
	batchDone    = domain.FeedBatchState("done")
	batchFailed  = domain.FeedBatchState("failed")
	actProposed  = domain.FeedActionState("proposed")
)

// fakeEvents is feed.db as serve reads it (feed.EventReader).
type fakeEvents struct {
	events []domain.FeedEvent // ascending id
	mutes  []domain.FeedMute
}

func (f *fakeEvents) After(_ context.Context, afterID int64, limit int) ([]domain.FeedEvent, error) {
	var out []domain.FeedEvent
	for _, e := range f.events {
		if e.ID > afterID && (limit <= 0 || len(out) < limit) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeEvents) ByIDs(_ context.Context, ids []int64) ([]domain.FeedEvent, error) {
	var out []domain.FeedEvent
	for _, e := range f.events {
		if slices.Contains(ids, e.ID) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeEvents) Thread(_ context.Context, key string, beforeID int64, limit int) ([]domain.FeedEvent, error) {
	var out []domain.FeedEvent
	for _, e := range f.events {
		if e.ThreadKey == key && e.ID < beforeID {
			out = append(out, e)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func (f *fakeEvents) Mutes(_ context.Context, now time.Time) ([]domain.FeedMute, error) {
	var out []domain.FeedMute
	for _, m := range f.mutes {
		if m.Until.After(now) {
			out = append(out, m)
		}
	}
	return out, nil
}

// fakeStore is serve's feed tables (feed.Store).
type fakeStore struct {
	mu        sync.Mutex
	batches   []domain.FeedBatch
	highWater int64
	hwHistory []int64
	actions   []domain.FeedAction
	holds     map[string]int64
}

func newFakeStore() *fakeStore { return &fakeStore{holds: map[string]int64{}} }

func (s *fakeStore) CutFeedBatch(_ context.Context, b domain.FeedBatch) (domain.FeedBatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b.ID = int64(len(s.batches) + 1)
	s.batches = append(s.batches, b)
	s.highWater = b.UpToEventID
	s.hwHistory = append(s.hwHistory, b.UpToEventID)
	return b, nil
}

func (s *fakeStore) FeedHighWater(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.highWater, nil
}

func (s *fakeStore) UpdateFeedBatch(_ context.Context, b domain.FeedBatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.batches {
		if s.batches[i].ID == b.ID {
			s.batches[i] = b
			return nil
		}
	}
	return errors.New("no such batch")
}

func (s *fakeStore) GetFeedBatch(_ context.Context, id int64) (domain.FeedBatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.batches {
		if b.ID == id {
			return b, nil
		}
	}
	return domain.FeedBatch{}, errors.New("no such batch")
}

func (s *fakeStore) ListFeedBatches(_ context.Context, limit int) ([]domain.FeedBatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.FeedBatch
	for i := len(s.batches) - 1; i >= 0 && (limit <= 0 || len(out) < limit); i-- {
		out = append(out, s.batches[i])
	}
	return out, nil
}

func (s *fakeStore) RunningFeedBatches(context.Context) ([]domain.FeedBatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.FeedBatch
	for _, b := range s.batches {
		if b.State == batchRunning {
			out = append(out, b)
		}
	}
	return out, nil
}

func (s *fakeStore) DailyFeedBatches(_ context.Context, since time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, b := range s.batches {
		if !b.StartedAt.Before(since) {
			n++
		}
	}
	return n, nil
}

func (s *fakeStore) InsertFeedAction(_ context.Context, a domain.FeedAction) (domain.FeedAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a.ID = int64(len(s.actions) + 1)
	s.actions = append(s.actions, a)
	return a, nil
}

func (s *fakeStore) UpdateFeedAction(_ context.Context, a domain.FeedAction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.actions {
		if s.actions[i].ID == a.ID {
			s.actions[i] = a
			return nil
		}
	}
	return errors.New("no such action")
}

func (s *fakeStore) GetFeedAction(_ context.Context, id int64) (domain.FeedAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.actions {
		if a.ID == id {
			return a, nil
		}
	}
	return domain.FeedAction{}, errors.New("no such action")
}

func (s *fakeStore) ListFeedActions(_ context.Context, states []domain.FeedActionState, limit int) ([]domain.FeedAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.FeedAction
	for i := len(s.actions) - 1; i >= 0 && (limit <= 0 || len(out) < limit); i-- {
		if len(states) == 0 || slices.Contains(states, s.actions[i].State) {
			out = append(out, s.actions[i])
		}
	}
	return out, nil
}

func (s *fakeStore) OpenFeedActionEventFloor(context.Context) (int64, error) { return 0, nil }

func (s *fakeStore) HoldFeedThread(_ context.Context, key string, until int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.holds[key] = until
	return nil
}

func (s *fakeStore) FeedThreadHeld(_ context.Context, key string, eventID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	until, ok := s.holds[key]
	return ok && eventID <= until, nil
}

// fakeJobs runs the agent: it keeps each job and its batch.md as written.
type fakeJobs struct {
	jobs    []agentjob.Job
	batchMD []string
	answer  func(batchMD string) (string, error)
}

func (f *fakeJobs) run(_ context.Context, j agentjob.Job) (agentjob.JobResult, error) {
	b, _ := os.ReadFile(filepath.Join(j.Dir, "batch.md"))
	f.jobs = append(f.jobs, j)
	f.batchMD = append(f.batchMD, string(b))
	out := `{"actions":[],"digest":[],"memory_ops":[],"summary":"nothing"}`
	if f.answer != nil {
		var err error
		if out, err = f.answer(string(b)); err != nil {
			return agentjob.JobResult{}, err
		}
	}
	return agentjob.JobResult{Stdout: out, Agent: j.Agent, Model: j.Model}, nil
}

type managerRig struct {
	events *fakeEvents
	store  *fakeStore
	jobs   *fakeJobs
	now    time.Time
	opts   feed.ManagerOptions
}

func newRig(t *testing.T) *managerRig {
	t.Helper()
	r := &managerRig{events: &fakeEvents{}, store: newFakeStore(), jobs: &fakeJobs{}, now: feedT0}
	r.opts = feed.ManagerOptions{
		Events:       r.events,
		Store:        r.store,
		RunJob:       r.jobs.run,
		Now:          func() time.Time { return r.now },
		Dir:          t.TempDir(),
		Agent:        "claude",
		Interval:     5 * time.Minute,
		UrgentGap:    time.Minute,
		DailyBatches: 150,
	}
	return r
}

func (r *managerRig) addEvents(from, to int64, mod func(*domain.FeedEvent)) {
	for i := from; i <= to; i++ {
		r.events.events = append(r.events.events, ev(i, mod))
	}
}

func (r *managerRig) tick(t *testing.T) {
	t.Helper()
	m, err := feed.NewManager(r.opts)
	if err != nil {
		t.Fatal(err)
	}
	_ = m.Tick(context.Background()) // a failed batch may surface as an error
}

func (r *managerRig) checkMarkNeverBack(t *testing.T) {
	t.Helper()
	for i := 1; i < len(r.store.hwHistory); i++ {
		if r.store.hwHistory[i] < r.store.hwHistory[i-1] {
			t.Errorf("high water moved back: %v", r.store.hwHistory)
		}
	}
}

func TestTickWithoutEventsRunsNothing(t *testing.T) {
	r := newRig(t)
	r.tick(t)
	if len(r.jobs.jobs) != 0 || len(r.store.batches) != 0 {
		t.Errorf("runs %d, batches %d; want none", len(r.jobs.jobs), len(r.store.batches))
	}
}

func TestTickCutsAtMost30Unmuted(t *testing.T) {
	r := newRig(t)
	r.addEvents(1, 45, nil)
	r.events.mutes = []domain.FeedMute{{Scope: domain.FeedMuteScope("thread"), Key: "slack:C1/3", Until: feedT0.Add(time.Hour)}}
	r.tick(t)
	if len(r.store.batches) != 1 || len(r.jobs.jobs) != 1 {
		t.Fatalf("batches %d, runs %d; want 1 each", len(r.store.batches), len(r.jobs.jobs))
	}
	b := r.store.batches[0]
	want := []int64{1, 2}
	for i := int64(4); i <= 31; i++ {
		want = append(want, i)
	}
	if !slices.Equal(b.EventIDs, want) {
		t.Errorf("EventIDs = %v, want the oldest 30 unmuted %v", b.EventIDs, want)
	}
	if b.UpToEventID != 31 || r.store.highWater != 31 {
		t.Errorf("UpToEventID %d, high water %d; want 31", b.UpToEventID, r.store.highWater)
	}
	if b.State != batchDone {
		t.Errorf("state = %s, want done", b.State)
	}
	if strings.Contains(r.jobs.batchMD[0], "body-3;") || strings.Contains(r.jobs.batchMD[0], "body-32;") {
		t.Error("batch.md holds a muted event or one past the cut")
	}

	r.now = r.now.Add(5 * time.Minute)
	r.tick(t)
	if len(r.store.batches) != 2 || r.store.highWater != 45 || len(r.store.batches[1].EventIDs) != 14 {
		t.Errorf("second batch = %+v, high water %d; want events 32-45", r.store.batches[len(r.store.batches)-1], r.store.highWater)
	}
	r.checkMarkNeverBack(t)
}

func TestTickSchedule(t *testing.T) {
	cases := []struct {
		name   string
		urgent bool
		after  time.Duration
		runs   bool
	}{
		{"before the interval", false, time.Minute, false},
		{"at the interval", false, 5 * time.Minute, true},
		{"urgent within the gap", true, 30 * time.Second, false},
		{"urgent after the gap", true, 61 * time.Second, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.addEvents(1, 10, nil)
			r.store.batches = []domain.FeedBatch{{ID: 1, EventIDs: []int64{10}, UpToEventID: 10, State: batchDone, Attempts: 1, StartedAt: feedT0.Add(-time.Minute), EndedAt: feedT0}}
			r.store.highWater = 10
			r.addEvents(11, 11, func(e *domain.FeedEvent) { e.Urgent = c.urgent })
			r.now = feedT0.Add(c.after)
			r.tick(t)
			if ran := len(r.jobs.jobs) == 1; ran != c.runs {
				t.Errorf("ran = %v, want %v", ran, c.runs)
			}
		})
	}
}

func TestTickOneBatchInFlight(t *testing.T) {
	r := newRig(t)
	r.addEvents(1, 5, nil)
	r.store.batches = []domain.FeedBatch{{ID: 1, EventIDs: []int64{1}, UpToEventID: 1, State: batchRunning, Attempts: 1, StartedAt: feedT0.Add(-time.Hour)}}
	r.store.highWater = 1
	r.now = feedT0.Add(time.Hour)
	r.tick(t)
	if len(r.jobs.jobs) != 0 || len(r.store.batches) != 1 {
		t.Errorf("runs %d, batches %d while one runs; want no new run", len(r.jobs.jobs), len(r.store.batches))
	}
}

func TestTickRetriesAFailedBatchAlone(t *testing.T) {
	r := newRig(t)
	r.addEvents(1, 6, nil)
	r.store.batches = []domain.FeedBatch{{ID: 1, EventIDs: []int64{1, 2, 3}, UpToEventID: 3, State: batchFailed, Attempts: 1, StartedAt: feedT0.Add(-time.Hour), EndedAt: feedT0.Add(-time.Hour)}}
	r.store.highWater = 3
	r.tick(t)
	if len(r.jobs.jobs) != 1 {
		t.Fatalf("runs = %d, want 1", len(r.jobs.jobs))
	}
	md := r.jobs.batchMD[0]
	for _, id := range []int{1, 2, 3} {
		if !strings.Contains(md, fmt.Sprintf("body-%d;", id)) {
			t.Errorf("retry lacks event %d", id)
		}
	}
	if strings.Contains(md, "body-4;") {
		t.Error("retry carries a new event; it runs alone")
	}
	b := r.store.batches[0]
	if len(r.store.batches) != 1 || b.Attempts != 2 || b.State != batchDone || !slices.Equal(b.EventIDs, []int64{1, 2, 3}) {
		t.Errorf("batches = %+v; want batch 1 retried with the same events", r.store.batches)
	}
	if r.store.highWater != 3 {
		t.Errorf("high water = %d, want 3 until the retry is done", r.store.highWater)
	}
}

func TestTickSecondFailureMovesOn(t *testing.T) {
	r := newRig(t)
	r.addEvents(1, 3, nil)
	r.jobs.answer = func(md string) (string, error) {
		if strings.Contains(md, "body-1;") {
			return "", errors.New("agent exited 1")
		}
		return `{"actions":[],"summary":"ok"}`, nil
	}
	r.tick(t)
	r.addEvents(4, 5, nil)
	for range 3 {
		r.now = r.now.Add(5 * time.Minute)
		r.tick(t)
	}
	if len(r.store.batches) == 0 {
		t.Fatal("no batch cut")
	}
	first := r.store.batches[0]
	if first.State != batchFailed || first.Attempts != 2 {
		t.Errorf("batch 1 = %s after %d attempts, want failed after 2", first.State, first.Attempts)
	}
	runsOfFirst := 0
	for _, md := range r.jobs.batchMD {
		if strings.Contains(md, "body-1;") {
			runsOfFirst++
		}
	}
	if runsOfFirst != 2 {
		t.Errorf("batch 1 ran %d times, want 2 (one retry)", runsOfFirst)
	}
	if len(r.store.batches) != 2 || !slices.Equal(r.store.batches[1].EventIDs, []int64{4, 5}) || r.store.batches[1].State != batchDone {
		t.Errorf("batches = %+v; want a second batch of 4 and 5", r.store.batches)
	}
	r.checkMarkNeverBack(t)
}

func TestTickDailyCap(t *testing.T) {
	for _, c := range []struct {
		cap  int
		runs bool
	}{{2, false}, {3, true}, {0, true}} {
		t.Run(fmt.Sprint(c.cap), func(t *testing.T) {
			r := newRig(t)
			r.opts.DailyBatches = c.cap
			r.addEvents(1, 3, nil)
			r.store.batches = []domain.FeedBatch{
				{ID: 1, EventIDs: []int64{1}, UpToEventID: 1, State: batchDone, Attempts: 1, StartedAt: feedT0.Add(-2 * time.Hour), EndedAt: feedT0.Add(-2 * time.Hour)},
				{ID: 2, EventIDs: []int64{2}, UpToEventID: 2, State: batchDone, Attempts: 1, StartedAt: feedT0.Add(-time.Hour), EndedAt: feedT0.Add(-time.Hour)},
			}
			r.store.highWater = 2
			r.tick(t)
			if ran := len(r.jobs.jobs) == 1; ran != c.runs {
				t.Errorf("daily_batches %d: ran = %v, want %v", c.cap, ran, c.runs)
			}
		})
	}
}

func TestTickValidatesActions(t *testing.T) {
	r := newRig(t)
	r.addEvents(1, 3, nil)
	r.store.holds["slack:C1/2"] = 2
	read := func(answer int) string {
		return fmt.Sprintf(`{"kind":"read","priority":"normal","rationale":"r%d","answers":[%d],"options":[{"label":"open","params":{"url":"https://example.com/%d","why":"w"}}]}`, answer, answer, answer)
	}
	r.store.actions = []domain.FeedAction{{
		ID: 1, BatchID: 0, Kind: domain.FeedRead, Rationale: "r3", Priority: domain.FeedPriority("normal"), Answers: []int64{3},
		Options: []domain.FeedOption{{Label: "open", Params: []byte(`{"url":"https://example.com/3","why":"w"}`)}},
		State:   actProposed, CreatedAt: feedT0.Add(-time.Hour), ExpiresAt: feedT0.Add(23 * time.Hour),
	}}
	steer := `{"kind":"steer_session","priority":"high","rationale":"phase 2","answers":[1],"options":[{"label":"go","params":{"session":"s1","text":"t"}}]}`
	r.jobs.answer = func(string) (string, error) {
		return `{"actions":[` + strings.Join([]string{read(1), read(999), steer, read(2), read(3)}, ",") + `],"summary":"s"}`, nil
	}
	r.tick(t)
	if len(r.store.actions) != 2 {
		t.Fatalf("stored %d actions, want the seeded one and one new: %+v", len(r.store.actions), r.store.actions)
	}
	a := r.store.actions[1]
	if !slices.Equal(a.Answers, []int64{1}) || a.Kind != domain.FeedRead || a.State != actProposed {
		t.Errorf("stored action = %+v; want the read answering event 1", a)
	}
	if a.BatchID != r.store.batches[0].ID || len(a.Trace) == 0 || !a.ExpiresAt.After(r.now) {
		t.Errorf("stored action batch %d, trace %d entries, expires %s", a.BatchID, len(a.Trace), a.ExpiresAt)
	}
	if r.store.batches[0].State != batchDone {
		t.Errorf("batch state = %s; dropped actions do not fail the batch", r.store.batches[0].State)
	}
}

func TestRecoverFailsRunningThenRetries(t *testing.T) {
	r := newRig(t)
	r.addEvents(1, 2, nil)
	r.store.batches = []domain.FeedBatch{{ID: 1, EventIDs: []int64{1, 2}, UpToEventID: 2, State: batchRunning, Attempts: 1, StartedAt: feedT0.Add(-time.Hour)}}
	r.store.highWater = 2
	m, err := feed.NewManager(r.opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := r.store.batches[0].State; s != batchFailed {
		t.Fatalf("after Recover state = %s, want failed", s)
	}
	_ = m.Tick(context.Background())
	b := r.store.batches[0]
	if len(r.jobs.jobs) != 1 || b.State != batchDone || b.Attempts != 2 || len(r.store.batches) != 1 {
		t.Errorf("after Tick batches = %+v, runs %d; want batch 1 retried", r.store.batches, len(r.jobs.jobs))
	}
}
