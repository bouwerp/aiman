package server

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/todo"
)

const testSteerWindow = 20 * time.Millisecond

type fakeSteerStore struct {
	mu        sync.Mutex
	recs      []domain.SteerRecord
	nextID    int64
	delivered map[string]time.Time
	deleted   []string
}

func (f *fakeSteerStore) AddSteerNote(_ context.Context, n *domain.SteerRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	n.ID = f.nextID
	f.recs = append(f.recs, *n)
	return nil
}

func (f *fakeSteerStore) ListUndeliveredSteerNotes(context.Context) ([]domain.SteerRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.SteerRecord
	for _, r := range f.recs {
		if r.DeliveredAt.IsZero() {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeSteerStore) MarkSteerNotesDelivered(_ context.Context, ids []int64, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.recs {
		if slices.Contains(ids, f.recs[i].ID) {
			f.recs[i].DeliveredAt = at
		}
	}
	return nil
}

func (f *fakeSteerStore) DeleteSteerNotes(_ context.Context, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, sessionID)
	f.recs = slices.DeleteFunc(f.recs, func(r domain.SteerRecord) bool { return r.SessionID == sessionID })
	return nil
}

func (f *fakeSteerStore) LastSteerDelivery(context.Context) (map[string]time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.delivered, nil
}

func (f *fakeSteerStore) records() []domain.SteerRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.recs)
}

type fakeSteerTarget struct {
	mu         sync.Mutex
	ready      bool
	held       string
	deliverErr error
	submitAt   time.Time
	delivered  []string
	list       domain.TodoList
}

func (f *fakeSteerTarget) steerReady(context.Context, string, time.Time) (bool, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ready, f.held
}

func (f *fakeSteerTarget) deliverSteer(_ context.Context, _, text string) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deliverErr != nil {
		return time.Time{}, f.deliverErr
	}
	f.delivered = append(f.delivered, text)
	return f.submitAt, nil
}

func (f *fakeSteerTarget) todoList(string) (domain.TodoList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.list, nil
}

func (f *fakeSteerTarget) setReady(ready bool, held string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ready, f.held = ready, held
}

func (f *fakeSteerTarget) deliveries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.delivered)
}

type eventLog struct {
	mu     sync.Mutex
	events []SessionEvent
}

func (l *eventLog) publish(ev SessionEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, ev)
}

func (l *eventLog) all() []SessionEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

// runSteerer starts Run with a long tick so tests drive delivery with Kick.
func runSteerer(t *testing.T, s *steerer) {
	t.Helper()
	s.tick = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func waitQueued(t *testing.T, s *steerer, id string, n int) {
	t.Helper()
	eventually(t, 2*time.Second, func() bool { return s.Status(id).Queued == n })
}

func TestSteererCoalescesWithinWindow(t *testing.T) {
	store := &fakeSteerStore{}
	target := &fakeSteerTarget{list: domain.TodoList{Items: []domain.TodoItem{{ID: "t1", Text: "write tests", State: domain.TodoPending}}}}
	s := newSteerer(store, target, nil, testSteerWindow)

	s.EnqueueTodo("s1", domain.AuthorUser, []todo.Op{{Kind: todo.OpAdd, ID: "t1", Text: "write tests"}})
	s.EnqueueTodo("s1", domain.AuthorVerifier, []todo.Op{{Kind: todo.OpState, ID: "t1", State: domain.TodoActive}})
	s.Enqueue(SteerNote{SessionID: "s1", Author: domain.AuthorUser, Text: "please hurry"})
	s.Enqueue(SteerNote{SessionID: "s2", Author: domain.AuthorUser, Text: "other session"})

	waitQueued(t, s, "s1", 2)
	waitQueued(t, s, "s2", 1)
	var recs []domain.SteerRecord
	for _, r := range store.records() {
		if r.SessionID == "s1" {
			recs = append(recs, r)
		}
	}
	if len(recs) != 2 {
		t.Fatalf("records = %d, want 2: %+v", len(recs), recs)
	}
	note := recs[0].Text
	if !strings.Contains(note, "+ t1 write tests") || !strings.Contains(note, "▸ t1 active") {
		t.Errorf("todo note misses an op:\n%s", note)
	}
	if !strings.Contains(note, "user and verifier") {
		t.Errorf("todo note authors:\n%s", note)
	}
	if recs[1].Text != "please hurry" {
		t.Errorf("free text note = %q", recs[1].Text)
	}
}

func TestSteererBuffersUntilWindowCloses(t *testing.T) {
	store := &fakeSteerStore{}
	s := newSteerer(store, &fakeSteerTarget{}, nil, time.Hour)
	s.Enqueue(SteerNote{SessionID: "s1", Author: domain.AuthorUser, Text: "later"})
	time.Sleep(5 * testSteerWindow)
	if recs := store.records(); len(recs) != 0 {
		t.Fatalf("flushed before the window closed: %+v", recs)
	}
	s.Forget("s1")
}

func TestSteererHoldsUntilReady(t *testing.T) {
	store := &fakeSteerStore{}
	target := &fakeSteerTarget{held: "working"}
	s := newSteerer(store, target, nil, testSteerWindow)
	runSteerer(t, s)

	s.Enqueue(SteerNote{SessionID: "s1", Author: domain.AuthorUser, Text: "hello"})
	waitQueued(t, s, "s1", 1)
	s.Kick("s1")
	eventually(t, 2*time.Second, func() bool { return s.Status("s1").Held == "working" })
	if got := target.deliveries(); len(got) != 0 {
		t.Fatalf("delivered while held: %q", got)
	}
	if st := s.Status("s1"); st.Queued != 1 {
		t.Fatalf("Queued = %d, want 1", st.Queued)
	}

	target.setReady(true, "")
	s.Kick("s1")
	waitQueued(t, s, "s1", 0)
	s.Kick("s1")
	time.Sleep(3 * testSteerWindow)
	if got := target.deliveries(); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("deliveries = %q, want one hello", got)
	}
	if st := s.Status("s1"); st.Held != "" || st.LastDeliveredAt.IsZero() {
		t.Errorf("status after delivery = %+v", st)
	}
}

func TestSteererDeliversAllQueuedTogether(t *testing.T) {
	store := &fakeSteerStore{}
	target := &fakeSteerTarget{held: "working"}
	s := newSteerer(store, target, nil, testSteerWindow)
	runSteerer(t, s)

	s.Enqueue(SteerNote{SessionID: "s1", Author: domain.AuthorUser, Text: "first"})
	waitQueued(t, s, "s1", 1)
	s.Enqueue(SteerNote{SessionID: "s1", Author: domain.AuthorUser, Text: "second"})
	waitQueued(t, s, "s1", 2)

	target.setReady(true, "")
	s.Kick("s1")
	waitQueued(t, s, "s1", 0)
	got := target.deliveries()
	if len(got) != 1 || got[0] != "first\n\nsecond" {
		t.Fatalf("deliveries = %q", got)
	}
	for _, r := range store.records() {
		if r.DeliveredAt.IsZero() {
			t.Errorf("record %d not marked delivered", r.ID)
		}
	}
}

func TestSteererReloadsUndeliveredOnce(t *testing.T) {
	store := &fakeSteerStore{}
	if err := store.AddSteerNote(context.Background(), &domain.SteerRecord{SessionID: "s1", Author: domain.AuthorUser, Text: "left over", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	target := &fakeSteerTarget{ready: true}
	s := newSteerer(store, target, nil, testSteerWindow)
	for range 2 {
		if err := s.Load(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if st := s.Status("s1"); st.Queued != 1 {
		t.Fatalf("Queued after two loads = %d, want 1", st.Queued)
	}
	runSteerer(t, s)
	s.Kick("s1")
	waitQueued(t, s, "s1", 0)
	s.Kick("s1")
	time.Sleep(3 * testSteerWindow)
	if got := target.deliveries(); len(got) != 1 || got[0] != "left over" {
		t.Fatalf("deliveries = %q, want exactly one", got)
	}
}

func TestSteererRecordsDeliveryError(t *testing.T) {
	store := &fakeSteerStore{}
	target := &fakeSteerTarget{ready: true, deliverErr: errors.New("pane gone")}
	s := newSteerer(store, target, nil, testSteerWindow)
	runSteerer(t, s)

	s.Enqueue(SteerNote{SessionID: "s1", Author: domain.AuthorUser, Text: "hello"})
	waitQueued(t, s, "s1", 1)
	s.Kick("s1")
	eventually(t, 2*time.Second, func() bool { return s.Status("s1").LastError != "" })
	st := s.Status("s1")
	if !strings.Contains(st.LastError, "pane gone") || st.Queued != 1 {
		t.Fatalf("status = %+v", st)
	}
	if recs := store.records(); len(recs) != 1 || !recs[0].DeliveredAt.IsZero() {
		t.Fatalf("records = %+v", recs)
	}
}

func TestSteererPublishesSteerEvents(t *testing.T) {
	store := &fakeSteerStore{}
	target := &fakeSteerTarget{ready: true}
	log := &eventLog{}
	s := newSteerer(store, target, log.publish, testSteerWindow)
	runSteerer(t, s)

	s.Enqueue(SteerNote{SessionID: "s1", Author: domain.AuthorUser, Text: "hello"})
	eventually(t, 2*time.Second, func() bool { return len(log.all()) >= 1 })
	s.Kick("s1")
	eventually(t, 2*time.Second, func() bool { return len(log.all()) >= 2 })

	evs := log.all()
	first, second := evs[0], evs[1]
	if first.Type != "steer" || first.ID != "s1" || first.Queued == nil || *first.Queued != 1 || first.DeliveredAt != "" {
		t.Errorf("queued event = %+v", first)
	}
	if second.Type != "steer" || second.ID != "s1" || second.Queued == nil || *second.Queued != 0 || second.DeliveredAt == "" {
		t.Errorf("delivered event = %+v", second)
	}
	if _, err := time.Parse(time.RFC3339Nano, second.DeliveredAt); err != nil {
		t.Errorf("DeliveredAt %q: %v", second.DeliveredAt, err)
	}
}

func TestEnqueueTodoIgnoresRefOnly(t *testing.T) {
	store := &fakeSteerStore{}
	log := &eventLog{}
	s := newSteerer(store, &fakeSteerTarget{}, log.publish, testSteerWindow)

	s.EnqueueTodo("s1", domain.AuthorUser, []todo.Op{{Kind: todo.OpRef, ID: "t1", Ref: "child:x"}})
	s.EnqueueTodo("s1", domain.AuthorAgent, []todo.Op{{Kind: todo.OpAdd, ID: "t2", Text: "mine"}})
	s.Enqueue(SteerNote{SessionID: "s1", Author: domain.AuthorAgent, Text: "self"})
	time.Sleep(5 * testSteerWindow)

	if recs := store.records(); len(recs) != 0 {
		t.Fatalf("records = %+v", recs)
	}
	if evs := log.all(); len(evs) != 0 {
		t.Fatalf("events = %+v", evs)
	}
	if st := s.Status("s1"); st.Queued != 0 {
		t.Fatalf("Queued = %d", st.Queued)
	}
}

func TestSteerStatusListsPending(t *testing.T) {
	store := &fakeSteerStore{}
	s := newSteerer(store, &fakeSteerTarget{held: "dialog"}, nil, testSteerWindow)

	s.Enqueue(SteerNote{SessionID: "s1", Author: domain.AuthorUser, Text: "older"})
	waitQueued(t, s, "s1", 1)
	s.Enqueue(SteerNote{SessionID: "s1", Author: domain.AuthorAiman, Text: "newer"})
	waitQueued(t, s, "s1", 2)

	want := []SteerNote{
		{SessionID: "s1", Author: domain.AuthorUser, Text: "older"},
		{SessionID: "s1", Author: domain.AuthorAiman, Text: "newer"},
	}
	if got := s.Status("s1").Pending; !slices.Equal(got, want) {
		t.Fatalf("Pending = %+v, want %+v", got, want)
	}
}

func TestSteererLoadSeedsLastDelivered(t *testing.T) {
	at := time.Date(2026, 10, 22, 9, 30, 0, 0, time.UTC)
	store := &fakeSteerStore{delivered: map[string]time.Time{"s1": at}}
	s := newSteerer(store, &fakeSteerTarget{}, nil, testSteerWindow)
	if err := s.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := s.Status("s1").LastDeliveredAt; !got.Equal(at) {
		t.Fatalf("LastDeliveredAt = %v, want %v", got, at)
	}
}

func TestSteererForgetDropsQueue(t *testing.T) {
	store := &fakeSteerStore{}
	target := &fakeSteerTarget{held: "working"}
	s := newSteerer(store, target, nil, testSteerWindow)

	s.Enqueue(SteerNote{SessionID: "s1", Author: domain.AuthorUser, Text: "queued"})
	waitQueued(t, s, "s1", 1)
	s.Enqueue(SteerNote{SessionID: "s1", Author: domain.AuthorUser, Text: "in window"})
	s.Forget("s1")
	time.Sleep(5 * testSteerWindow)

	if st := s.Status("s1"); st.Queued != 0 || len(st.Pending) != 0 {
		t.Fatalf("status after forget = %+v", st)
	}
	if recs := store.records(); len(recs) != 0 {
		t.Fatalf("records after forget = %+v", recs)
	}
}

func TestSteererNilKickAndForget(t *testing.T) {
	var s *steerer
	s.Kick("s1")
	s.Forget("s1")
}

// The delivery time is when the note was submitted, not when the target
// finished confirming it: agent reports in between are about this note.
func TestSteererStampsSubmitTime(t *testing.T) {
	submitAt := time.Now().Add(-10 * time.Second)
	store := &fakeSteerStore{recs: []domain.SteerRecord{{ID: 1, SessionID: "s1", Author: domain.AuthorUser, Text: "n"}}, nextID: 1}
	target := &fakeSteerTarget{ready: true, submitAt: submitAt}
	s := newSteerer(store, target, nil, testSteerWindow)
	if err := s.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.deliver(context.Background(), "s1")
	if got := s.Status("s1").LastDeliveredAt; !got.Equal(submitAt) {
		t.Fatalf("LastDeliveredAt = %v, want the submit time %v", got, submitAt)
	}
	if got := store.records()[0].DeliveredAt; !got.Equal(submitAt) {
		t.Fatalf("stored DeliveredAt = %v, want %v", got, submitAt)
	}
}
