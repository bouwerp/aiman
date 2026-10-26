package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func eventRepo(t *testing.T) *Repository {
	t.Helper()
	repo, err := NewRepository(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

func TestAppendSessionEventAssignsSeqAndHash(t *testing.T) {
	repo := eventRepo(t)
	ctx := context.Background()
	first, err := repo.AppendSessionEvent(ctx, domain.SessionEvent{
		SessionID: "s1",
		Actor:     domain.EventActorUser,
		Kind:      domain.EventSessionCreated,
		Payload:   json.RawMessage(`{"issue":"A"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.AppendSessionEvent(ctx, domain.SessionEvent{
		SessionID: "s1",
		Actor:     domain.EventActorAgent,
		Kind:      domain.EventHookReported,
		Payload:   json.RawMessage(`{"state":"idle"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if first != 1 || second != 2 {
		t.Fatalf("seqs = %d, %d, want 1, 2", first, second)
	}
	events, err := repo.ListSessionEvents(ctx, "s1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d", len(events))
	}
	if events[0].Kind != domain.EventSessionCreated || events[0].Hash == "" || events[0].At.IsZero() {
		t.Fatalf("first = %+v", events[0])
	}
	if events[1].Actor != domain.EventActorAgent || string(events[1].Payload) != `{"state":"idle"}` {
		t.Fatalf("second = %+v", events[1])
	}
	since, err := repo.ListSessionEvents(ctx, "s1", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(since) != 1 || since[0].Seq != 2 {
		t.Fatalf("since = %+v", since)
	}
}

func TestAppendSessionEventSeparateSessions(t *testing.T) {
	repo := eventRepo(t)
	ctx := context.Background()
	for _, id := range []string{"s1", "s2", "s1"} {
		if _, err := repo.AppendSessionEvent(ctx, domain.SessionEvent{
			SessionID: id,
			Actor:     domain.EventActorAiman,
			Kind:      domain.EventSessionCreated,
		}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := repo.ListSessionEvents(ctx, "s1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.ListSessionEvents(ctx, "s2", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].Seq != 1 || first[1].Seq != 2 {
		t.Fatalf("s1 = %+v", first)
	}
	if len(second) != 1 || second[0].Seq != 1 {
		t.Fatalf("s2 = %+v", second)
	}
}

func TestListSessionEventsLimit(t *testing.T) {
	repo := eventRepo(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := repo.AppendSessionEvent(ctx, domain.SessionEvent{
			SessionID: "s1",
			Actor:     domain.EventActorUser,
			Kind:      domain.EventTodoChanged,
		}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := repo.ListSessionEvents(ctx, "s1", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("events = %+v", events)
	}
}

func TestAppendSessionEventRejectsBadPayload(t *testing.T) {
	repo := eventRepo(t)
	_, err := repo.AppendSessionEvent(context.Background(), domain.SessionEvent{
		SessionID: "s1",
		Actor:     domain.EventActorUser,
		Kind:      domain.EventSessionCreated,
		Payload:   json.RawMessage(`not json`),
	})
	if err == nil {
		t.Fatal("invalid payload accepted")
	}
	events, listErr := repo.ListSessionEvents(context.Background(), "s1", 0, 10)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(events) != 0 {
		t.Fatalf("invalid event was stored: %+v", events)
	}
}

func TestAppendSessionEventDefaults(t *testing.T) {
	repo := eventRepo(t)
	seq, err := repo.AppendSessionEvent(context.Background(), domain.SessionEvent{
		SessionID: "s1",
		Kind:      domain.EventSessionEnded,
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err := repo.ListSessionEvents(context.Background(), "s1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Seq != seq {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Actor != domain.EventActorUnknown {
		t.Fatalf("actor = %q", events[0].Actor)
	}
	if events[0].At.IsZero() || events[0].Hash == "" {
		t.Fatalf("defaults = %+v", events[0])
	}
}

func TestAppendSessionEventConcurrentSequences(t *testing.T) {
	repo := eventRepo(t)
	ctx := context.Background()
	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.AppendSessionEvent(ctx, domain.SessionEvent{
				SessionID: "s1",
				Actor:     domain.EventActorAgent,
				Kind:      domain.EventHookReported,
				At:        time.Now().UTC(),
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	events, err := repo.ListSessionEvents(ctx, "s1", 0, n)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != n {
		t.Fatalf("events = %d, want %d", len(events), n)
	}
	for i, ev := range events {
		if ev.Seq != int64(i+1) {
			t.Fatalf("event %d seq = %d", i, ev.Seq)
		}
	}
}
