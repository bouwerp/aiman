//go:build feedaccept

package feedcollect_test

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/feedcollect"
	"github.com/bouwerp/aiman/internal/feeddb"
)

// Acceptance tests for the collector (P3a): one PollOnce stores a source's
// events and cursors with one Writer.Append, a failed poll keeps the old
// cursors and writes health, and live mutes reach the source.

var collectT0 = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

// clock is an injectable time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock(t time.Time) *clock { return &clock{t: t} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// pollResult is what fakeSource answers to one Poll.
type pollResult struct {
	events  []domain.FeedEvent
	cursors map[string]string
	err     error
}

// fakeSource answers Poll from a queue and records what it was given.
type fakeSource struct {
	name    domain.FeedSource
	answers []pollResult
	gotCurs []map[string]string
	gotMute [][]domain.FeedMute
}

func (f *fakeSource) Name() domain.FeedSource { return f.name }
func (f *fakeSource) Every() time.Duration    { return time.Minute }

func (f *fakeSource) Poll(_ context.Context, cursors map[string]string, mutes []domain.FeedMute) ([]domain.FeedEvent, map[string]string, error) {
	f.gotCurs = append(f.gotCurs, maps.Clone(cursors))
	f.gotMute = append(f.gotMute, append([]domain.FeedMute(nil), mutes...))
	if len(f.answers) == 0 {
		return nil, cursors, nil
	}
	a := f.answers[0]
	f.answers = f.answers[1:]
	return a.events, a.cursors, a.err
}

func ghEvent(ext string, at time.Time) domain.FeedEvent {
	return domain.FeedEvent{
		Source: "github", ExternalID: ext, At: at, Actor: "octo", Kind: "mention",
		Title: "t " + ext, Body: "b " + ext, ThreadKey: "gh:o/r#" + ext, Class: "direct", Urgent: true,
	}
}

type collectEnv struct {
	path   string
	writer *feeddb.Writer
	reader *feeddb.Reader
	clock  *clock
	c      *feedcollect.Collector
}

func newCollectEnv(t *testing.T, sources ...feedcollect.Source) *collectEnv {
	t.Helper()
	path := filepath.Join(t.TempDir(), "feed.db")
	w, err := feeddb.OpenWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	r, err := feeddb.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	clk := newClock(collectT0)
	c, err := feedcollect.New(feedcollect.Options{
		DB: w, Sources: sources, Now: clk.Now, Retention: 30 * 24 * time.Hour,
		Log: func(string, ...any) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &collectEnv{path: path, writer: w, reader: r, clock: clk, c: c}
}

func (e *collectEnv) poll(t *testing.T, name domain.FeedSource) error {
	t.Helper()
	return e.c.PollOnce(context.Background(), name)
}

func (e *collectEnv) events(t *testing.T) []domain.FeedEvent {
	t.Helper()
	evs, err := e.reader.After(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func (e *collectEnv) cursors(t *testing.T, src domain.FeedSource) map[string]string {
	t.Helper()
	c, err := e.writer.Cursors(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (e *collectEnv) health(t *testing.T, src domain.FeedSource) feeddb.Health {
	t.Helper()
	hs, err := e.reader.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hs {
		if h.Source == src {
			return h
		}
	}
	t.Fatalf("no health row for %s in %+v", src, hs)
	return feeddb.Health{}
}

func TestPollOnceStoresEventsAndCursors(t *testing.T) {
	src := &fakeSource{name: "github", answers: []pollResult{
		{events: []domain.FeedEvent{ghEvent("1", collectT0), ghEvent("2", collectT0)}, cursors: map[string]string{"since": "v1"}},
		{cursors: map[string]string{"since": "v2"}},
	}}
	env := newCollectEnv(t, src)

	if err := env.poll(t, "github"); err != nil {
		t.Fatal(err)
	}
	evs := env.events(t)
	if len(evs) != 2 || evs[0].ExternalID != "1" || evs[1].ExternalID != "2" || evs[0].ID == 0 {
		t.Fatalf("stored = %+v", evs)
	}
	if got := env.cursors(t, "github"); got["since"] != "v1" {
		t.Fatalf("cursors = %v", got)
	}
	if h := env.health(t, "github"); h.Error != "" || !h.LastOK.Equal(collectT0) || !h.LastPoll.Equal(collectT0) {
		t.Fatalf("health = %+v", h)
	}

	env.clock.Add(time.Minute)
	if err := env.poll(t, "github"); err != nil {
		t.Fatal(err)
	}
	if len(src.gotCurs) != 2 || src.gotCurs[0]["since"] != "" || src.gotCurs[1]["since"] != "v1" {
		t.Fatalf("cursors given to Poll = %v", src.gotCurs)
	}
	if got := env.cursors(t, "github"); got["since"] != "v2" {
		t.Fatalf("cursors after second poll = %v", got)
	}
}

func TestPollOnceDedupesAcrossPolls(t *testing.T) {
	src := &fakeSource{name: "github", answers: []pollResult{
		{events: []domain.FeedEvent{ghEvent("1", collectT0)}, cursors: map[string]string{"since": "v1"}},
		{events: []domain.FeedEvent{ghEvent("1", collectT0), ghEvent("3", collectT0)}, cursors: map[string]string{"since": "v2"}},
	}}
	env := newCollectEnv(t, src)
	for range 2 {
		if err := env.poll(t, "github"); err != nil {
			t.Fatal(err)
		}
	}
	evs := env.events(t)
	if len(evs) != 2 || evs[0].ExternalID != "1" || evs[1].ExternalID != "3" {
		t.Fatalf("stored = %+v, want 1 and 3 once each", evs)
	}
}

func TestPollOnceErrorKeepsCursorsAndWritesHealth(t *testing.T) {
	src := &fakeSource{name: "github", answers: []pollResult{
		{events: []domain.FeedEvent{ghEvent("1", collectT0)}, cursors: map[string]string{"since": "v1"}},
		{events: []domain.FeedEvent{ghEvent("2", collectT0)}, cursors: map[string]string{"since": "v2"}, err: errors.New("gh: HTTP 502")},
		{cursors: map[string]string{"since": "v3"}},
	}}
	env := newCollectEnv(t, src)
	if err := env.poll(t, "github"); err != nil {
		t.Fatal(err)
	}

	env.clock.Add(2 * time.Minute)
	if err := env.poll(t, "github"); err == nil {
		t.Fatal("PollOnce hid the source error")
	}
	if got := env.cursors(t, "github"); got["since"] != "v1" {
		t.Fatalf("a failed poll moved the cursors: %v", got)
	}
	if evs := env.events(t); len(evs) != 1 {
		t.Fatalf("a failed poll stored events: %+v", evs)
	}
	h := env.health(t, "github")
	if h.Error == "" || !h.LastPoll.Equal(collectT0.Add(2*time.Minute)) || !h.LastOK.Equal(collectT0) {
		t.Fatalf("health after failure = %+v", h)
	}

	env.clock.Add(2 * time.Minute)
	if err := env.poll(t, "github"); err != nil {
		t.Fatal(err)
	}
	if src.gotCurs[2]["since"] != "v1" {
		t.Fatalf("poll after failure got cursors %v, want the kept v1", src.gotCurs[2])
	}
	if h := env.health(t, "github"); h.Error != "" || !h.LastOK.Equal(collectT0.Add(4*time.Minute)) {
		t.Fatalf("health after recovery = %+v", h)
	}
}

func TestPollOncePassesLiveMutes(t *testing.T) {
	src := &fakeSource{name: "github"}
	env := newCollectEnv(t, src)
	mw, err := feeddb.OpenMuteWriter(env.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mw.Close() })
	ctx := context.Background()
	live := domain.FeedMute{Scope: "thread", Key: "gh:o/r#7", Until: collectT0.Add(time.Hour), ActionID: 1}
	gone := domain.FeedMute{Scope: "thread", Key: "gh:o/r#8", Until: collectT0.Add(-time.Minute), ActionID: 2}
	for _, m := range []domain.FeedMute{live, gone} {
		if err := mw.Mute(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := env.poll(t, "github"); err != nil {
		t.Fatal(err)
	}
	if len(src.gotMute) != 1 || len(src.gotMute[0]) != 1 || src.gotMute[0][0].Key != "gh:o/r#7" {
		t.Fatalf("mutes given to Poll = %+v, want only the live one", src.gotMute)
	}
}

func TestPollOnceUnknownSource(t *testing.T) {
	env := newCollectEnv(t, &fakeSource{name: "github"})
	if err := env.poll(t, "slack"); err == nil {
		t.Fatal("PollOnce of a source the collector does not have succeeded")
	}
}

func TestNewRefusesMissingDB(t *testing.T) {
	if _, err := feedcollect.New(feedcollect.Options{Sources: []feedcollect.Source{&fakeSource{name: "github"}}}); err == nil {
		t.Fatal("New without a database succeeded")
	}
}
