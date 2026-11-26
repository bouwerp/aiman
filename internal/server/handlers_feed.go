package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/feed"
	"github.com/bouwerp/aiman/internal/feeddb"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
)

// FeedEvents is feed.db as the feed RPCs read it: the manager's reader plus
// the collector's health. Every method answers feeddb.ErrNoDatabase while
// the collector has not created the file.
type FeedEvents interface {
	feed.EventReader
	Health(ctx context.Context) ([]feeddb.Health, error)
}

// FeedUnmuter removes a mute from feed.db.
type FeedUnmuter interface {
	Unmute(ctx context.Context, scope domain.FeedMuteScope, key string) error
}

// FeedDeps is what the feed.* RPCs need. With Enabled false they answer
// feed_status with enabled false and unavailable for the rest.
type FeedDeps struct {
	Enabled bool
	Store   feed.Store
	Events  FeedEvents
	Mutes   FeedUnmuter
	// Act holds the executors and the mute writer; Store, Events and Now
	// are filled from this struct.
	Act    feed.ActDeps
	Memory *feed.Memory
	// Dir is the manager's directory (digest lines live under it).
	Dir string
	// Settings is the feed config block as serve read it.
	Settings any
	Now      func() time.Time
}

// SetFeed wires the feed RPCs.
func (s *Server) SetFeed(d FeedDeps) {
	if d.Now == nil {
		d.Now = time.Now
	}
	s.feed.Store(&d)
}

// FeedBatchChanged publishes the hub event for a batch change.
func (s *Server) FeedBatchChanged(b domain.FeedBatch) {
	s.hub.Publish(SessionEvent{Type: "feed", Batch: b.ID, State: string(b.State)})
}

// FeedActionChanged publishes the hub event for an action change, and a
// feed_action notification when a high-priority action is proposed.
func (s *Server) FeedActionChanged(a domain.FeedAction) {
	s.hub.Publish(SessionEvent{Type: "feed", Action: a.ID, State: string(a.State)})
	if a.State != feed.ActionProposed || a.Priority != "high" {
		return
	}
	rec := s.NotifyRecorder()
	if rec == nil {
		return
	}
	detail := fmt.Sprintf("feed: %s: %s", a.Kind, firstLine(a.Rationale))
	if err := rec.Emit(context.Background(), "", "", domain.NotifyFeedAction, detail); err != nil {
		log.Printf("feed: notify: %v", err)
	}
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// FeedContext is context.md's session facts: the visible root sessions,
// narrowed to groups when any are given, with their open to-do texts.
func (s *Server) FeedContext(ctx context.Context, groups []string) ([]feed.SessionFacts, error) {
	sessions, err := s.loadSessions(ctx)
	if err != nil {
		return nil, err
	}
	var keep []domain.Session
	todos := map[string][]string{}
	for _, sess := range sessions {
		if len(groups) > 0 && !slices.Contains(groups, sess.Group) {
			continue
		}
		if sess.AgentEnded {
			continue
		}
		keep = append(keep, sess)
		if s.todos == nil {
			continue
		}
		list, err := s.todos.Get(sess.ID)
		if err != nil {
			continue
		}
		for _, it := range list.Items {
			if it.State == domain.TodoPending || it.State == domain.TodoActive {
				todos[sess.ID] = append(todos[sess.ID], it.Text)
			}
		}
	}
	return feed.SessionFactsFrom(keep, todos), nil
}

// dispatchFeed routes the feed.* methods.
func (s *Server) dispatchFeed(ctx context.Context, req Request) Response {
	d := s.feed.Load()
	if req.Method == "feed.status" {
		return s.handleFeedStatus(ctx, req, d)
	}
	if d == nil || !d.Enabled || d.Store == nil {
		return errResp(req.ID, CodeUnavailable, "the feed is off on this host (feed.enabled)")
	}
	switch req.Method {
	case "feed.actions":
		return handleFeedActions(ctx, req, d)
	case "feed.batches":
		return handleFeedBatches(ctx, req, d)
	case "feed.events":
		return handleFeedEvents(ctx, req, d)
	case "feed.memory":
		return feedMemoryResp(ctx, req, d)
	default:
		return s.handleFeedAct(ctx, req, d)
	}
}

func feedParams(req Request, p any) *Response {
	if len(req.Params) == 0 {
		return nil
	}
	if err := json.Unmarshal(req.Params, p); err != nil {
		r := errResp(req.ID, CodeInvalidParams, err.Error())
		return &r
	}
	return nil
}

func feedErr(req Request, err error) Response {
	if errors.Is(err, feeddb.ErrNoDatabase) {
		return errResp(req.ID, CodeUnavailable, "the feed collector has not started (no feed.db yet)")
	}
	return errResp(req.ID, CodeUnavailable, err.Error())
}

func (s *Server) handleFeedStatus(ctx context.Context, req Request, d *FeedDeps) Response {
	out := map[string]any{"type": "feed_status", "enabled": d != nil && d.Enabled}
	if d == nil || !d.Enabled || d.Store == nil {
		out["collector"] = map[string]any{"state": "off", "health": []feeddb.Health{}}
		if d != nil {
			out["settings"] = d.Settings
		}
		return Response{ID: req.ID, Result: out}
	}
	out["settings"] = d.Settings
	now := d.Now()
	collector := map[string]any{"state": "not_started", "health": []feeddb.Health{}}
	if d.Events != nil {
		health, err := d.Events.Health(ctx)
		switch {
		case errors.Is(err, feeddb.ErrNoDatabase):
		case err != nil:
			collector["state"], collector["error"] = "error", err.Error()
		default:
			collector["health"] = nonNilHealth(health)
			collector["state"] = collectorState(health, now)
		}
	}
	out["collector"] = collector
	hw, err := d.Store.FeedHighWater(ctx)
	if err != nil {
		return feedErr(req, err)
	}
	out["high_water"] = hw
	pending := 0
	if d.Events != nil {
		if evs, err := d.Events.After(ctx, hw, 1000); err == nil {
			pending = len(evs)
		}
	}
	out["pending"] = pending
	open, err := d.Store.ListFeedActions(ctx, feed.OpenStates(), 1000)
	if err != nil {
		return feedErr(req, err)
	}
	high := 0
	for _, a := range open {
		if a.Priority == "high" && a.State == feed.ActionProposed {
			high++
		}
	}
	out["open_actions"], out["high_actions"] = len(open), high
	if bs, err := d.Store.ListFeedBatches(ctx, 1); err == nil && len(bs) == 1 {
		out["last_batch"] = bs[0]
	}
	y, mo, dd := now.Date()
	if n, err := d.Store.DailyFeedBatches(ctx, time.Date(y, mo, dd, 0, 0, 0, 0, now.Location())); err == nil {
		out["batches_today"] = n
	}
	return Response{ID: req.ID, Result: out}
}

func nonNilHealth(h []feeddb.Health) []feeddb.Health {
	if h == nil {
		return []feeddb.Health{}
	}
	return h
}

// collectorState is "error" when a source's last poll failed, "stale" when
// no source polled in the last 10 minutes, else "ok".
func collectorState(health []feeddb.Health, now time.Time) string {
	if len(health) == 0 {
		return "starting"
	}
	latest := time.Time{}
	for _, h := range health {
		if h.Error != "" {
			return "error"
		}
		if h.LastPoll.After(latest) {
			latest = h.LastPoll
		}
	}
	if now.Sub(latest) > 10*time.Minute {
		return "stale"
	}
	return "ok"
}

func handleFeedActions(ctx context.Context, req Request, d *FeedDeps) Response {
	var p struct {
		State []domain.FeedActionState `json:"state"`
		ID    int64                    `json:"id"`
		Limit int                      `json:"limit"`
	}
	if r := feedParams(req, &p); r != nil {
		return *r
	}
	if p.ID != 0 {
		a, err := d.Store.GetFeedAction(ctx, p.ID)
		if err != nil {
			return errResp(req.ID, CodeNotFound, fmt.Sprintf("no feed action %d", p.ID))
		}
		return Response{ID: req.ID, Result: map[string]any{"type": "feed_actions", "actions": []domain.FeedAction{a}}}
	}
	limit := clampLimit(p.Limit, 100, 1000)
	acts, err := d.Store.ListFeedActions(ctx, p.State, limit)
	if err != nil {
		return feedErr(req, err)
	}
	if acts == nil {
		acts = []domain.FeedAction{}
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "feed_actions", "actions": acts}}
}

// feedBatchPager is the store's batch history page; the repository has it.
type feedBatchPager interface {
	FeedBatchesPage(ctx context.Context, limit int, before int64) ([]sqlite.FeedBatchPage, error)
}

func handleFeedBatches(ctx context.Context, req Request, d *FeedDeps) Response {
	var p struct {
		Limit  int   `json:"limit"`
		Before int64 `json:"before"`
	}
	if r := feedParams(req, &p); r != nil {
		return *r
	}
	pager, ok := d.Store.(feedBatchPager)
	if !ok {
		return errResp(req.ID, CodeUnavailable, "the feed store has no batch history")
	}
	limit := clampLimit(p.Limit, 20, 200)
	bs, err := pager.FeedBatchesPage(ctx, limit, p.Before)
	if err != nil {
		return feedErr(req, err)
	}
	if bs == nil {
		bs = []sqlite.FeedBatchPage{}
	}
	var next int64
	if len(bs) == limit {
		next = bs[len(bs)-1].ID
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "feed_batches", "batches": bs, "next_before": next}}
}

func clampLimit(n, def, max int) int {
	if n <= 0 {
		return def
	}
	return min(n, max)
}

// handleFeedEvents answers events by ids, by batch, or with digest true the
// newest batches' events no action answered, plus every event of a batch that
// failed twice (unprocessed). A digest line the agent wrote replaces the
// event's title.
func handleFeedEvents(ctx context.Context, req Request, d *FeedDeps) Response {
	var p struct {
		Batch  int64   `json:"batch"`
		IDs    []int64 `json:"ids"`
		Digest bool    `json:"digest"`
		Limit  int     `json:"limit"`
	}
	if r := feedParams(req, &p); r != nil {
		return *r
	}
	if d.Events == nil {
		return feedErr(req, feeddb.ErrNoDatabase)
	}
	limit := clampLimit(p.Limit, 200, 1000)
	var evs []domain.FeedEvent
	var err error
	switch {
	case len(p.IDs) > 0:
		evs, err = d.Events.ByIDs(ctx, p.IDs)
	case p.Batch != 0:
		var b domain.FeedBatch
		if b, err = d.Store.GetFeedBatch(ctx, p.Batch); err != nil {
			return errResp(req.ID, CodeNotFound, fmt.Sprintf("no feed batch %d", p.Batch))
		}
		evs, err = d.Events.ByIDs(ctx, b.EventIDs)
	case p.Digest:
		evs, err = feedDigest(ctx, d, limit)
	default:
		return errResp(req.ID, CodeInvalidParams, "feed.events needs ids, batch or digest")
	}
	if err != nil {
		return feedErr(req, err)
	}
	if len(evs) > limit {
		evs = evs[:limit]
	}
	if evs == nil {
		evs = []domain.FeedEvent{}
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "feed_events", "events": evs}}
}

func feedDigest(ctx context.Context, d *FeedDeps, limit int) ([]domain.FeedEvent, error) {
	batches, err := d.Store.ListFeedBatches(ctx, 10)
	if err != nil {
		return nil, err
	}
	acts, err := d.Store.ListFeedActions(ctx, nil, 1000)
	if err != nil {
		return nil, err
	}
	answered := map[int64]bool{}
	for _, a := range acts {
		for _, id := range a.Answers {
			answered[id] = true
		}
	}
	var out []domain.FeedEvent
	for _, b := range batches {
		if b.State == feed.BatchRunning || (b.State == feed.BatchFailed && b.Attempts < 2) {
			continue
		}
		evs, err := d.Events.ByIDs(ctx, b.EventIDs)
		if err != nil {
			return nil, err
		}
		lines := feed.ReadDigestLines(d.Dir, b.ID)
		for _, e := range evs {
			if b.State == feed.BatchDone && answered[e.ID] {
				continue
			}
			if l, ok := lines[e.ID]; ok {
				e.Title = l
			}
			out = append(out, e)
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func feedMemoryResp(ctx context.Context, req Request, d *FeedDeps) Response {
	bullets := []feed.MemoryBullet{}
	if d.Memory != nil {
		bs, err := d.Memory.Bullets()
		if err != nil {
			return feedErr(req, err)
		}
		if bs != nil {
			bullets = bs
		}
	}
	mutes := []domain.FeedMute{}
	if d.Events != nil {
		ms, err := d.Events.Mutes(ctx, d.Now())
		if err != nil && !errors.Is(err, feeddb.ErrNoDatabase) {
			return feedErr(req, err)
		}
		if ms != nil {
			mutes = ms
		}
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "feed_memory", "bullets": bullets, "mutes": mutes}}
}

// handleFeedAct runs feed.act.
func (s *Server) handleFeedAct(ctx context.Context, req Request, d *FeedDeps) Response {
	if s.isSessionCall(ctx, req) {
		return errResp(req.ID, CodeNeedsOperator, "only the operator acts on the feed")
	}
	var p struct {
		ID       int64                    `json:"id"`
		Op       string                   `json:"op"`
		MemoryID string                   `json:"memory_id"`
		Scope    string                   `json:"scope"`
		Key      string                   `json:"key"`
		Option   *int                     `json:"option"`
		Params   json.RawMessage          `json:"params"`
		Reason   domain.FeedDismissReason `json:"reason"`
		Note     string                   `json:"note"`
		Until    time.Time                `json:"until"`
	}
	if r := feedParams(req, &p); r != nil {
		return *r
	}
	switch p.Op {
	case "approve", "dismiss":
		return s.feedDecide(ctx, req, d, p.ID, feed.ActRequest{
			Op: p.Op, Option: p.Option, Params: p.Params, Reason: p.Reason, Note: p.Note, Until: p.Until, By: "operator",
		})
	case "forget_memory":
		if p.MemoryID == "" || d.Memory == nil {
			return errResp(req.ID, CodeInvalidParams, "forget_memory needs memory_id")
		}
		ok, err := d.Memory.Forget(p.MemoryID)
		if err != nil {
			return feedErr(req, err)
		}
		if !ok {
			return errResp(req.ID, CodeNotFound, "no memory bullet "+p.MemoryID)
		}
	case "unmute":
		switch p.Scope {
		case "thread", "sender", "channel":
		default:
			return errResp(req.ID, CodeInvalidParams, "unmute needs scope thread, sender or channel")
		}
		if p.Key == "" {
			return errResp(req.ID, CodeInvalidParams, "unmute needs key")
		}
		if d.Mutes == nil {
			return feedErr(req, feeddb.ErrNoDatabase)
		}
		if err := d.Mutes.Unmute(ctx, domain.FeedMuteScope(p.Scope), p.Key); err != nil {
			return feedErr(req, err)
		}
	default:
		return errResp(req.ID, CodeInvalidParams, fmt.Sprintf("feed.act op %q is not available on this serve", p.Op))
	}
	s.hub.Publish(SessionEvent{Type: "feed", State: "memory"})
	return feedMemoryResp(ctx, req, d)
}

// feedDecide runs approve or dismiss through feed.Act and publishes the
// change.
func (s *Server) feedDecide(ctx context.Context, req Request, d *FeedDeps, id int64, ar feed.ActRequest) Response {
	if id == 0 {
		return errResp(req.ID, CodeInvalidParams, "feed.act needs id")
	}
	if _, err := d.Store.GetFeedAction(ctx, id); err != nil {
		return errResp(req.ID, CodeNotFound, fmt.Sprintf("no feed action %d", id))
	}
	deps := d.Act
	deps.Store, deps.Now = d.Store, d.Now
	if d.Events != nil {
		deps.Events = d.Events
	}
	deps.OnAction = s.FeedActionChanged
	a, err := feed.Act(ctx, deps, id, ar)
	if errors.Is(err, feed.ErrRefused) {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	if err != nil {
		return feedErr(req, err)
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "feed_action", "action": a}}
}

// FeedTodoAdd adds an open to-do, authored by aiman, to the visible session
// named by id or name.
func (s *Server) FeedTodoAdd(ctx context.Context, session, text string) error {
	text = oneLine(text)
	if text == "" {
		return errors.New("to-do text is empty")
	}
	sessions, err := s.loadSessions(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(sessions, func(x domain.Session) bool {
		return !x.Role.Hidden() && (x.ID == session || x.Name == session)
	})
	if i < 0 {
		return fmt.Errorf("no visible session %q", session)
	}
	_, err = s.MutateTodos(ctx, sessions[i], domain.AuthorAiman, func(l *domain.TodoList) error {
		l.Items = append(l.Items, domain.TodoItem{Text: text, State: domain.TodoPending})
		return nil
	})
	return err
}
