package feeddb_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/feeddb"
)

var feedT0 = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

func feedPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "feed.db")
}

func openWriter(t *testing.T, path string) *feeddb.Writer {
	t.Helper()
	w, err := feeddb.OpenWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func openReader(t *testing.T, path string) *feeddb.Reader {
	t.Helper()
	r, err := feeddb.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func openMuteWriter(t *testing.T, path string) *feeddb.MuteWriter {
	t.Helper()
	m, err := feeddb.OpenMuteWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func slackEvent(ext, thread string, at time.Time) domain.FeedEvent {
	return domain.FeedEvent{
		Source: "slack", ExternalID: ext, At: at, Actor: "U1", Kind: "message",
		Title: "t " + ext, Body: "b " + ext, ThreadKey: thread, Class: "direct",
	}
}

func appendOK(t *testing.T, w *feeddb.Writer, events ...domain.FeedEvent) []domain.FeedEvent {
	t.Helper()
	stored, err := w.Append(context.Background(), events, nil)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func ids(events []domain.FeedEvent) []int64 {
	out := make([]int64, len(events))
	for i, ev := range events {
		out[i] = ev.ID
	}
	return out
}

func TestFeedPath(t *testing.T) {
	if got, want := feeddb.Path("/h/.aiman"), filepath.Join("/h/.aiman", "feed.db"); got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

func TestFeedAppendDedupesAndSetsCursors(t *testing.T) {
	path := feedPath(t)
	w := openWriter(t, path)
	ctx := context.Background()
	key := feeddb.CursorKey{Source: "slack", Key: "C1"}

	first, err := w.Append(ctx, []domain.FeedEvent{
		slackEvent("C1/1.0", "slack:C1/1.0", feedT0),
		slackEvent("C1/2.0", "slack:C1/2.0", feedT0.Add(time.Second)),
	}, map[feeddb.CursorKey]string{key: "2.0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].ID == 0 || first[1].ID <= first[0].ID {
		t.Fatalf("first append stored %+v, want two events with rising ids", ids(first))
	}

	second, err := w.Append(ctx, []domain.FeedEvent{
		slackEvent("C1/2.0", "slack:C1/2.0", feedT0.Add(time.Second)),
		slackEvent("C1/3.0", "slack:C1/3.0", feedT0.Add(2*time.Second)),
		// Same external id from another source is a different event.
		{Source: "github", ExternalID: "C1/2.0", At: feedT0, Actor: "ana", Kind: "mention", Title: "x", Body: "y", ThreadKey: "gh:C1/2.0", Class: "direct"},
	}, map[feeddb.CursorKey]string{key: "3.0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 2 || second[0].ExternalID != "C1/3.0" || second[1].Source != "github" {
		t.Fatalf("second append stored %+v, want only the new slack event and the github one", second)
	}
	if second[0].ID <= first[1].ID {
		t.Fatalf("new id %d not above %d", second[0].ID, first[1].ID)
	}

	cursors, err := w.Cursors(ctx, "slack")
	if err != nil {
		t.Fatal(err)
	}
	if cursors["C1"] != "3.0" || len(cursors) != 1 {
		t.Fatalf("cursors = %v, want C1=3.0", cursors)
	}

	all, err := openReader(t, path).After(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("stored events = %d, want 4", len(all))
	}
}

func TestFeedFailedAppendLeavesCursors(t *testing.T) {
	path := feedPath(t)
	w := openWriter(t, path)
	key := feeddb.CursorKey{Source: "slack", Key: "C1"}
	if _, err := w.Append(context.Background(), nil, map[feeddb.CursorKey]string{key: "1.0"}); err != nil {
		t.Fatal(err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		events []domain.FeedEvent
	}{
		{"event without external id", context.Background(), []domain.FeedEvent{
			slackEvent("C1/5.0", "slack:C1/5.0", feedT0),
			slackEvent("", "slack:C1/6.0", feedT0),
		}},
		{"cancelled context", cancelled, []domain.FeedEvent{slackEvent("C1/7.0", "slack:C1/7.0", feedT0)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := w.Append(tc.ctx, tc.events, map[feeddb.CursorKey]string{key: "9.0"}); err == nil {
				t.Fatal("Append succeeded, want an error")
			}
			cursors, err := w.Cursors(context.Background(), "slack")
			if err != nil {
				t.Fatal(err)
			}
			if cursors["C1"] != "1.0" {
				t.Fatalf("cursor = %q after a failed append, want 1.0", cursors["C1"])
			}
			stored, err := openReader(t, path).After(context.Background(), 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(stored) != 0 {
				t.Fatalf("failed append stored %+v", stored)
			}
		})
	}
}

func TestFeedPruneRespectsFloorAndNeverReusesIDs(t *testing.T) {
	path := feedPath(t)
	w := openWriter(t, path)
	m := openMuteWriter(t, path)
	ctx := context.Background()

	old := feedT0.Add(-40 * 24 * time.Hour)
	stored := appendOK(t, w,
		slackEvent("a", "k", old),
		slackEvent("b", "k", old),
		slackEvent("c", "k", old),
		slackEvent("d", "k", feedT0), // newer than the cutoff
	)
	if err := m.SetFloor(ctx, stored[2].ID); err != nil {
		t.Fatal(err)
	}
	n, err := w.Prune(ctx, feedT0.Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("pruned %d, want 2 (below the floor and old)", n)
	}
	left, err := openReader(t, path).After(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(left); len(got) != 2 || got[0] != stored[2].ID || got[1] != stored[3].ID {
		t.Fatalf("left %v, want %v", got, []int64{stored[2].ID, stored[3].ID})
	}

	// Prune the newest id too, then append: the id is not reused.
	if err := m.SetFloor(ctx, 1<<60); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Prune(ctx, feedT0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	again := appendOK(t, w, slackEvent("e", "k", feedT0))
	if len(again) != 1 || again[0].ID <= stored[3].ID {
		t.Fatalf("new id %v after prune, want above %d", ids(again), stored[3].ID)
	}
}

func TestFeedReaderCannotWrite(t *testing.T) {
	path := feedPath(t)
	appendOK(t, openWriter(t, path), slackEvent("a", "k", feedT0))
	r := openReader(t, path)
	if _, err := r.DB().ExecContext(context.Background(), `DELETE FROM events`); err == nil {
		t.Fatal("delete through the reader succeeded, want a read-only error")
	}
	if _, err := r.DB().ExecContext(context.Background(), `INSERT INTO cursors(source, key, value) VALUES('x','y','z')`); err == nil {
		t.Fatal("insert through the reader succeeded, want a read-only error")
	}
}

func TestFeedOpenReaderMissingFile(t *testing.T) {
	_, err := feeddb.OpenReader(feedPath(t))
	if !errors.Is(err, feeddb.ErrNoDatabase) {
		t.Fatalf("OpenReader on a missing file: %v, want ErrNoDatabase", err)
	}
}

func TestFeedReaderAfterAndThread(t *testing.T) {
	path := feedPath(t)
	w := openWriter(t, path)
	var events []domain.FeedEvent
	for i := 0; i < 6; i++ {
		thread := "slack:C1/1.0"
		if i%2 == 1 {
			thread = "slack:C2/9.0"
		}
		events = append(events, slackEvent(fmt.Sprintf("e%d", i), thread, feedT0.Add(time.Duration(i)*time.Second)))
	}
	stored := appendOK(t, w, events...)
	r := openReader(t, path)
	ctx := context.Background()

	after, err := r.After(ctx, stored[1].ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(after); len(got) != 3 || got[0] != stored[2].ID || got[1] != stored[3].ID || got[2] != stored[4].ID {
		t.Fatalf("After(%d, 3) = %v", stored[1].ID, got)
	}
	if after[0].Title != "t e2" || !after[0].At.Equal(feedT0.Add(2*time.Second)) || after[0].ThreadKey != "slack:C1/1.0" {
		t.Fatalf("After row = %+v", after[0])
	}

	// Thread C1 holds e0, e2, e4. A limit keeps the newest events before beforeID.
	thread, err := r.Thread(ctx, "slack:C1/1.0", stored[5].ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(thread); len(got) != 3 || got[0] != stored[0].ID || got[1] != stored[2].ID || got[2] != stored[4].ID {
		t.Fatalf("Thread = %v, want e0, e2, e4 oldest first", got)
	}
	thread, err = r.Thread(ctx, "slack:C1/1.0", stored[4].ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(thread); len(got) != 1 || got[0] != stored[2].ID {
		t.Fatalf("Thread before e4 limit 1 = %v, want e2", got)
	}

	byIDs, err := r.ByIDs(ctx, []int64{stored[3].ID, stored[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(byIDs) != 2 {
		t.Fatalf("ByIDs = %v", ids(byIDs))
	}
}

func TestFeedHealthRoundTrip(t *testing.T) {
	path := feedPath(t)
	w := openWriter(t, path)
	h := feeddb.Health{Source: "github", LastPoll: feedT0, LastOK: feedT0.Add(-time.Minute), Error: "rate limited", Calls: 7}
	if err := w.SetHealth(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	got, err := openReader(t, path).Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Source != "github" || got[0].Error != "rate limited" || got[0].Calls != 7 ||
		!got[0].LastPoll.Equal(h.LastPoll) || !got[0].LastOK.Equal(h.LastOK) {
		t.Fatalf("health = %+v", got)
	}
}

func TestFeedMutesVisibleToWriterAndReader(t *testing.T) {
	path := feedPath(t)
	w := openWriter(t, path)
	m := openMuteWriter(t, path)
	r := openReader(t, path)
	ctx := context.Background()
	now := feedT0

	for _, mute := range []domain.FeedMute{
		{Scope: "thread", Key: "slack:C1/1.0", Until: now.Add(time.Hour), ActionID: 4},
		{Scope: "sender", Key: "U9", Until: now.Add(-time.Minute), ActionID: 5}, // expired
		{Scope: "channel", Key: "C7", Until: now.Add(24 * time.Hour), ActionID: 6},
	} {
		if err := m.Mute(ctx, mute); err != nil {
			t.Fatal(err)
		}
	}
	check := func(label string, got []domain.FeedMute, want ...string) {
		t.Helper()
		keys := map[string]bool{}
		for _, mu := range got {
			keys[string(mu.Scope)+":"+mu.Key] = true
		}
		if len(got) != len(want) {
			t.Fatalf("%s mutes = %+v, want %v", label, got, want)
		}
		for _, k := range want {
			if !keys[k] {
				t.Fatalf("%s mutes = %+v, missing %s", label, got, k)
			}
		}
	}
	wm, err := w.Mutes(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	check("writer", wm, "thread:slack:C1/1.0", "channel:C7")
	rm, err := r.Mutes(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	check("reader", rm, "thread:slack:C1/1.0", "channel:C7")

	if err := m.Unmute(ctx, "channel", "C7"); err != nil {
		t.Fatal(err)
	}
	wm, err = w.Mutes(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	check("writer after unmute", wm, "thread:slack:C1/1.0")
	if wm[0].ActionID != 4 || !wm[0].Until.Equal(now.Add(time.Hour)) {
		t.Fatalf("mute = %+v", wm[0])
	}
}

func TestFeedWriterAndMuteWriterConcurrent(t *testing.T) {
	path := feedPath(t)
	w := openWriter(t, path)
	m := openMuteWriter(t, path)
	ctx := context.Background()
	const n = 50

	var wg sync.WaitGroup
	errs := make(chan error, 3*n)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			key := feeddb.CursorKey{Source: "slack", Key: "C1"}
			if _, err := w.Append(ctx, []domain.FeedEvent{slackEvent(fmt.Sprintf("x%d", i), "k", feedT0)}, map[feeddb.CursorKey]string{key: fmt.Sprint(i)}); err != nil {
				errs <- fmt.Errorf("append %d: %w", i, err)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if err := m.Mute(ctx, domain.FeedMute{Scope: "sender", Key: fmt.Sprintf("U%d", i), Until: feedT0.Add(time.Hour), ActionID: int64(i)}); err != nil {
				errs <- fmt.Errorf("mute %d: %w", i, err)
			}
			if err := m.SetFloor(ctx, int64(i)); err != nil {
				errs <- fmt.Errorf("floor %d: %w", i, err)
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	mutes, err := w.Mutes(ctx, feedT0)
	if err != nil {
		t.Fatal(err)
	}
	if len(mutes) != n {
		t.Fatalf("mutes = %d, want %d", len(mutes), n)
	}
}
