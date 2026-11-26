package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/feed"
	"github.com/bouwerp/aiman/internal/feeddb"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
)

type feedFakeEvents struct {
	events []domain.FeedEvent
	mutes  []domain.FeedMute
	health []feeddb.Health
	noDB   bool
}

func (f *feedFakeEvents) err() error {
	if f.noDB {
		return feeddb.ErrNoDatabase
	}
	return nil
}

func (f *feedFakeEvents) After(_ context.Context, after int64, limit int) ([]domain.FeedEvent, error) {
	var out []domain.FeedEvent
	for _, e := range f.events {
		if e.ID > after {
			out = append(out, e)
		}
	}
	return out, f.err()
}

func (f *feedFakeEvents) ByIDs(_ context.Context, ids []int64) ([]domain.FeedEvent, error) {
	var out []domain.FeedEvent
	for _, e := range f.events {
		for _, id := range ids {
			if e.ID == id {
				out = append(out, e)
			}
		}
	}
	return out, f.err()
}

func (f *feedFakeEvents) Thread(context.Context, string, int64, int) ([]domain.FeedEvent, error) {
	return nil, f.err()
}

func (f *feedFakeEvents) Mutes(context.Context, time.Time) ([]domain.FeedMute, error) {
	return f.mutes, f.err()
}

func (f *feedFakeEvents) Health(context.Context) ([]feeddb.Health, error) { return f.health, f.err() }

type feedFakeUnmuter struct{ calls [][2]string }

func (f *feedFakeUnmuter) Unmute(_ context.Context, scope domain.FeedMuteScope, key string) error {
	f.calls = append(f.calls, [2]string{string(scope), key})
	return nil
}

type feedRig struct {
	srv    *Server
	repo   *sqlite.Repository
	events *feedFakeEvents
	mutes  *feedFakeUnmuter
	memory *feed.Memory
	dir    string
}

func newFeedRig(t *testing.T) *feedRig {
	t.Helper()
	repo, err := sqlite.NewRepository(filepath.Join(shortTempDir(t), "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	dir := t.TempDir()
	r := &feedRig{
		srv: New(nil, repo, nil, nil, nil, nil, "test"), repo: repo, dir: dir,
		events: &feedFakeEvents{}, mutes: &feedFakeUnmuter{}, memory: feed.NewMemory(feed.MemoryPath(dir)),
	}
	r.srv.SetFeed(FeedDeps{Enabled: true, Store: repo, Events: r.events, Mutes: r.mutes, Memory: r.memory, Dir: dir,
		Settings: map[string]any{"interval": "5m"}})
	return r
}

func feedEv(id int64) domain.FeedEvent {
	return domain.FeedEvent{ID: id, Source: "slack", ExternalID: "C1/1", Actor: "U1", Kind: "message", Body: "b", ThreadKey: "slack:C1/1", Class: "direct"}
}

func TestFeedOffAnswersStatusOnly(t *testing.T) {
	srv := New(nil, testRepo(t), nil, nil, nil, nil, "test")
	var st map[string]any
	rpcOK(t, srv, "", "feed.status", nil, &st)
	if st["type"] != "feed_status" || st["enabled"] != false {
		t.Errorf("status = %v", st)
	}
	if resp := rpc(t, srv, "", "feed.actions", nil); resp.Error == nil || resp.Error.Code != CodeUnavailable {
		t.Errorf("feed.actions with the feed off = %+v, want unavailable", resp.Error)
	}
}

func TestFeedStatusCounts(t *testing.T) {
	r := newFeedRig(t)
	ctx := context.Background()
	r.events.events = []domain.FeedEvent{feedEv(1), feedEv(2), feedEv(3)}
	r.events.health = []feeddb.Health{{Source: "slack", LastPoll: time.Now(), LastOK: time.Now()}}
	b, err := r.repo.CutFeedBatch(ctx, domain.FeedBatch{EventIDs: []int64{1}, UpToEventID: 1, State: "done", Attempts: 1, StartedAt: time.Now(), EndedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []domain.FeedPriority{"high", "normal"} {
		if _, err := r.repo.InsertFeedAction(ctx, domain.FeedAction{BatchID: b.ID, Kind: domain.FeedRead, Priority: p, Answers: []int64{1},
			Options: []domain.FeedOption{{Label: "o", Params: []byte(`{}`)}}, State: "proposed", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	var st struct {
		Enabled      bool             `json:"enabled"`
		Collector    map[string]any   `json:"collector"`
		HighWater    int64            `json:"high_water"`
		Pending      int              `json:"pending"`
		OpenActions  int              `json:"open_actions"`
		HighActions  int              `json:"high_actions"`
		LastBatch    domain.FeedBatch `json:"last_batch"`
		BatchesToday int              `json:"batches_today"`
		Settings     map[string]any   `json:"settings"`
	}
	rpcOK(t, r.srv, "", "feed.status", nil, &st)
	if !st.Enabled || st.HighWater != 1 || st.Pending != 2 || st.OpenActions != 2 || st.HighActions != 1 || st.LastBatch.ID != b.ID {
		t.Errorf("status = %+v", st)
	}
	if st.Collector["state"] != "ok" || st.Settings["interval"] != "5m" {
		t.Errorf("collector %v settings %v", st.Collector, st.Settings)
	}
	r.events.noDB = true
	rpcOK(t, r.srv, "", "feed.status", nil, &st)
	if st.Collector["state"] != "not_started" {
		t.Errorf("collector without feed.db = %v, want not_started", st.Collector)
	}
}

func TestFeedActionsAndEvents(t *testing.T) {
	r := newFeedRig(t)
	ctx := context.Background()
	r.events.events = []domain.FeedEvent{feedEv(1), feedEv(2)}
	b, _ := r.repo.CutFeedBatch(ctx, domain.FeedBatch{EventIDs: []int64{1, 2}, UpToEventID: 2, State: "done", Attempts: 1, StartedAt: time.Now()})
	a, err := r.repo.InsertFeedAction(ctx, domain.FeedAction{BatchID: b.ID, Kind: domain.FeedRead, Priority: "normal", Answers: []int64{1},
		Options: []domain.FeedOption{{Label: "o", Params: []byte(`{}`)}}, State: "proposed", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	var acts struct {
		Actions []domain.FeedAction `json:"actions"`
	}
	rpcOK(t, r.srv, "", "feed.actions", map[string]any{"id": a.ID}, &acts)
	if len(acts.Actions) != 1 || acts.Actions[0].ID != a.ID {
		t.Errorf("by id = %+v", acts)
	}
	rpcOK(t, r.srv, "", "feed.actions", map[string]any{"state": []string{"done"}}, &acts)
	if len(acts.Actions) != 0 {
		t.Errorf("state done = %+v", acts)
	}
	if resp := rpc(t, r.srv, "", "feed.actions", map[string]any{"id": 999}); resp.Error == nil || resp.Error.Code != CodeNotFound {
		t.Errorf("unknown id = %+v", resp.Error)
	}
	var evs struct {
		Events []domain.FeedEvent `json:"events"`
	}
	rpcOK(t, r.srv, "", "feed.events", map[string]any{"batch": b.ID}, &evs)
	if len(evs.Events) != 2 {
		t.Errorf("batch events = %+v", evs)
	}
	rpcOK(t, r.srv, "", "feed.events", map[string]any{"digest": true}, &evs)
	if len(evs.Events) != 1 || evs.Events[0].ID != 2 {
		t.Errorf("digest = %+v, want the unanswered event 2", evs)
	}
}

func TestFeedActMemoryAndUnmute(t *testing.T) {
	r := newFeedRig(t)
	if _, err := r.memory.Apply([]feed.MemoryOp{{Op: "add", Text: "one"}, {Op: "add", Text: "two"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	var mem struct {
		Type    string              `json:"type"`
		Bullets []feed.MemoryBullet `json:"bullets"`
	}
	rpcOK(t, r.srv, "", "feed.act", map[string]any{"op": "forget_memory", "memory_id": "m1"}, &mem)
	if mem.Type != "feed_memory" || len(mem.Bullets) != 1 || mem.Bullets[0].ID != "m2" {
		t.Errorf("after forget = %+v", mem)
	}
	rpcOK(t, r.srv, "", "feed.act", map[string]any{"op": "unmute", "scope": "thread", "key": "slack:C1/1"}, &mem)
	if len(r.mutes.calls) != 1 || r.mutes.calls[0] != [2]string{"thread", "slack:C1/1"} {
		t.Errorf("unmute calls = %v", r.mutes.calls)
	}
	for _, op := range []string{"approve", "dismiss"} {
		if resp := rpc(t, r.srv, "", "feed.act", map[string]any{"id": 1, "op": op}); resp.Error == nil || resp.Error.Code != CodeInvalidParams {
			t.Errorf("%s = %+v, want invalid_params until P8", op, resp.Error)
		}
	}
	if resp := rpc(t, r.srv, "sess-1", "feed.act", map[string]any{"op": "forget_memory", "memory_id": "m2"}); resp.Error == nil || resp.Error.Code != CodeNeedsOperator {
		t.Errorf("session caller = %+v, want needs_operator", resp.Error)
	}
}

func TestFeedDigestIDsResolve(t *testing.T) {
	r := newFeedRig(t)
	ctx := context.Background()
	d1, err := r.repo.SaveFeedDigest(ctx, []int64{5, 6}, domain.FeedEvent{ID: 6, Kind: feed.DigestKind, Channel: "C1", Body: "x"})
	if err != nil {
		t.Fatal(err)
	}
	d2, err := r.repo.SaveFeedDigest(ctx, []int64{7}, domain.FeedEvent{ID: 7, Kind: feed.DigestKind, Channel: "C2"})
	if err != nil {
		t.Fatal(err)
	}
	if d1.ID != -1 || d2.ID != -2 {
		t.Fatalf("digest ids %d, %d; want -1, -2", d1.ID, d2.ID)
	}
	rd := feed.DigestReader{EventReader: &feedFakeEvents{events: []domain.FeedEvent{feedEv(3)}}, Digests: r.repo}
	evs, err := rd.ByIDs(ctx, []int64{3, -2, -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 || evs[0].ID != 3 || evs[1].ID != -2 || evs[2].ID != -1 || evs[2].Channel != "C1" {
		t.Errorf("ByIDs = %+v", evs)
	}
}

func TestFeedHighActionNotifies(t *testing.T) {
	r := newFeedRig(t)
	sub, cancel := r.srv.hub.Subscribe()
	defer cancel()
	r.srv.FeedActionChanged(domain.FeedAction{ID: 4, Kind: domain.FeedRead, Priority: "high", State: "proposed"})
	select {
	case ev := <-sub:
		if ev.Type != "feed" || ev.Action != 4 || ev.State != "proposed" {
			t.Errorf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no feed hub event")
	}
}
