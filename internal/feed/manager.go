package feed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/agentjob"
	"github.com/bouwerp/aiman/internal/domain"
)

// EventReader is feed.db as serve reads it.
type EventReader interface {
	After(ctx context.Context, afterID int64, limit int) ([]domain.FeedEvent, error)
	ByIDs(ctx context.Context, ids []int64) ([]domain.FeedEvent, error)
	Thread(ctx context.Context, threadKey string, beforeID int64, limit int) ([]domain.FeedEvent, error)
	Mutes(ctx context.Context, now time.Time) ([]domain.FeedMute, error)
}

// Store is serve's feed tables (the repository's feed methods).
type Store interface {
	CutFeedBatch(ctx context.Context, b domain.FeedBatch) (domain.FeedBatch, error)
	FeedHighWater(ctx context.Context) (int64, error)
	UpdateFeedBatch(ctx context.Context, b domain.FeedBatch) error
	GetFeedBatch(ctx context.Context, id int64) (domain.FeedBatch, error)
	ListFeedBatches(ctx context.Context, limit int) ([]domain.FeedBatch, error)
	RunningFeedBatches(ctx context.Context) ([]domain.FeedBatch, error)
	DailyFeedBatches(ctx context.Context, since time.Time) (int, error)
	InsertFeedAction(ctx context.Context, a domain.FeedAction) (domain.FeedAction, error)
	UpdateFeedAction(ctx context.Context, a domain.FeedAction) error
	GetFeedAction(ctx context.Context, id int64) (domain.FeedAction, error)
	ListFeedActions(ctx context.Context, states []domain.FeedActionState, limit int) ([]domain.FeedAction, error)
	OpenFeedActionEventFloor(ctx context.Context) (int64, error)
	HoldFeedThread(ctx context.Context, threadKey string, untilEventID int64) error
	FeedThreadHeld(ctx context.Context, threadKey string, eventID int64) (bool, error)
}

// ManagerOptions are the manager's seams and settings.
type ManagerOptions struct {
	Events EventReader
	Store  Store
	RunJob func(ctx context.Context, j agentjob.Job) (agentjob.JobResult, error)
	Now    func() time.Time
	// Dir is ~/.aiman/feed: batch directories and memory.md.
	Dir, Agent, Model   string
	Interval, UrgentGap time.Duration
	// DailyBatches caps the batches cut per day; 0 is no cap.
	DailyBatches int
	// RunTimeout bounds one run; zero is agentjob's default.
	RunTimeout time.Duration
	// ActionTTL is how long a proposed action waits; zero is 24h.
	ActionTTL time.Duration
	// Digests stores digest events with negative ids; nil keeps them only for
	// the batch.
	Digests DigestStore
	// Context lists the operator's visible sessions for context.md; nil
	// writes none and skips the visible-session check of todo_add.
	Context func(ctx context.Context) ([]SessionFacts, error)
	// SetFloor publishes the prune floor to feed.db; nil skips it.
	SetFloor func(ctx context.Context, minEventID int64) error
	// OffSources are sources switched off: their events never join a batch.
	OffSources []domain.FeedSource
	// Gate answers false while the host is under pressure; nil is open.
	Gate func() bool
	// OnBatch and OnAction report changes (hub event, notification).
	OnBatch  func(domain.FeedBatch)
	OnAction func(domain.FeedAction)
	Log      func(format string, args ...any)
}

// Batch and action states the manager writes.
const (
	BatchRunning = domain.FeedBatchState("running")
	BatchDone    = domain.FeedBatchState("done")
	BatchFailed  = domain.FeedBatchState("failed")

	ActionProposed  = domain.FeedActionState("proposed")
	ActionApproved  = domain.FeedActionState("approved")
	ActionRunning   = domain.FeedActionState("running")
	ActionDone      = domain.FeedActionState("done")
	ActionFailed    = domain.FeedActionState("failed")
	ActionDismissed = domain.FeedActionState("dismissed")
	ActionExpired   = domain.FeedActionState("expired")
	ActionSnoozed   = domain.FeedActionState("snoozed")
)

// OpenStates are the action states that still wait on the operator or run.
func OpenStates() []domain.FeedActionState {
	return []domain.FeedActionState{ActionProposed, ActionApproved, ActionRunning, ActionSnoozed}
}

const (
	maxBatchEvents  = 30
	readPage        = 500
	maxAttempts     = 2
	defaultTTL      = 24 * time.Hour
	tickEvery       = 15 * time.Second
	listAll         = 1000
	causeThreadRead = 200
)

// Manager cuts batches from feed.db, runs one agent call per batch and
// stores the actions it proposes.
type Manager struct {
	o      ManagerOptions
	memory *Memory
	mu     sync.Mutex // one Tick at a time
}

// NewManager checks the seams and fills defaults.
func NewManager(o ManagerOptions) (*Manager, error) {
	if o.Events == nil || o.Store == nil || o.RunJob == nil {
		return nil, errors.New("feed manager: Events, Store and RunJob are required")
	}
	if o.Dir == "" {
		return nil, errors.New("feed manager: Dir is required")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Interval <= 0 {
		o.Interval = 5 * time.Minute
	}
	if o.ActionTTL <= 0 {
		o.ActionTTL = defaultTTL
	}
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	return &Manager{o: o, memory: NewMemory(MemoryPath(o.Dir))}, nil
}

// MemoryPath is memory.md under the feed directory.
func MemoryPath(dir string) string { return filepath.Join(dir, "memory.md") }

// Memory is the manager's memory file.
func (m *Manager) Memory() *Memory { return m.memory }

// Recover marks every batch left running by an earlier serve failed, so the
// next Tick retries it.
func (m *Manager) Recover(ctx context.Context) error {
	running, err := m.o.Store.RunningFeedBatches(ctx)
	if err != nil {
		return err
	}
	for _, b := range running {
		b.State, b.Error, b.EndedAt = BatchFailed, "serve restarted during the run", m.o.Now()
		if err := m.o.Store.UpdateFeedBatch(ctx, b); err != nil {
			return err
		}
		m.batchChanged(b)
	}
	return nil
}

// Run recovers, then ticks until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	if err := m.Recover(ctx); err != nil {
		m.o.Log("feed: recover: %v", err)
	}
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	var last string
	for {
		if err := m.Tick(ctx); err != nil && ctx.Err() == nil && err.Error() != last {
			last = err.Error()
			m.o.Log("feed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick makes one scheduling decision: nothing while a batch runs; else retry
// the newest batch when it failed once; else cut and run a new batch when the
// interval (or, for an urgent event, the urgent gap) has passed since the
// newest batch ended. It also expires old actions and publishes the floor.
func (m *Manager) Tick(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expire(ctx)
	if _, err := WakeSnoozed(ctx, ActDeps{Store: m.o.Store, Now: m.o.Now, OnAction: m.o.OnAction, TTL: m.o.ActionTTL}); err != nil {
		m.o.Log("feed: wake snoozed: %v", err)
	}
	defer m.publishFloor(ctx)
	running, err := m.o.Store.RunningFeedBatches(ctx)
	if err != nil {
		return err
	}
	if len(running) > 0 {
		return nil
	}
	latest, err := m.o.Store.ListFeedBatches(ctx, 1)
	if err != nil {
		return err
	}
	if m.o.Gate != nil && !m.o.Gate() {
		return nil
	}
	if len(latest) == 1 && latest[0].State == BatchFailed && latest[0].Attempts < maxAttempts {
		return m.retry(ctx, latest[0])
	}
	now := m.o.Now()
	if m.o.DailyBatches > 0 {
		y, mo, d := now.Date()
		n, err := m.o.Store.DailyFeedBatches(ctx, time.Date(y, mo, d, 0, 0, 0, 0, now.Location()))
		if err != nil {
			return err
		}
		if n >= m.o.DailyBatches {
			return nil
		}
	}
	events, err := m.pending(ctx)
	if err != nil || len(events) == 0 {
		return err
	}
	if len(latest) == 1 {
		ended := latest[0].EndedAt
		if ended.IsZero() {
			ended = latest[0].StartedAt
		}
		gap := now.Sub(ended)
		urgent := m.o.UrgentGap > 0 && gap >= m.o.UrgentGap && slices.ContainsFunc(events, func(e domain.FeedEvent) bool { return e.Urgent })
		if gap < m.o.Interval && !urgent {
			return nil
		}
	}
	ids := make([]int64, len(events))
	for i, e := range events {
		ids[i] = e.ID
	}
	b, err := m.o.Store.CutFeedBatch(ctx, domain.FeedBatch{
		EventIDs: ids, UpToEventID: ids[len(ids)-1], State: BatchRunning, Attempts: 1,
		Agent: m.o.Agent, Model: m.o.Model, StartedAt: now,
	})
	if err != nil {
		return err
	}
	m.batchChanged(b)
	return m.process(ctx, b, events)
}

// pending is the oldest maxBatchEvents unmuted events above the high-water
// mark that are not the operator's own and whose source is on.
func (m *Manager) pending(ctx context.Context) ([]domain.FeedEvent, error) {
	hw, err := m.o.Store.FeedHighWater(ctx)
	if err != nil {
		return nil, err
	}
	mutes, err := m.o.Events.Mutes(ctx, m.o.Now())
	if err != nil {
		return nil, err
	}
	var out []domain.FeedEvent
	for after := hw; len(out) < maxBatchEvents; {
		page, err := m.o.Events.After(ctx, after, readPage)
		if err != nil {
			return nil, err
		}
		for _, e := range page {
			if len(out) == maxBatchEvents {
				break
			}
			if !e.Own && !Muted(e, mutes) && !slices.Contains(m.o.OffSources, e.Source) {
				out = append(out, e)
			}
		}
		if len(page) < readPage {
			break
		}
		after = page[len(page)-1].ID
	}
	return out, nil
}

// Muted reports whether a mute covers e: its thread, its actor, or its
// channel.
func Muted(e domain.FeedEvent, mutes []domain.FeedMute) bool {
	for _, mu := range mutes {
		switch mu.Scope {
		case "thread":
			if mu.Key == e.ThreadKey {
				return true
			}
		case "sender":
			if mu.Key == e.Actor {
				return true
			}
		case "channel":
			if e.Channel != "" && mu.Key == e.Channel {
				return true
			}
		}
	}
	return false
}

func (m *Manager) retry(ctx context.Context, b domain.FeedBatch) error {
	events, err := m.o.Events.ByIDs(ctx, b.EventIDs)
	if err != nil {
		return err
	}
	slices.SortFunc(events, func(a, b domain.FeedEvent) int { return int(a.ID - b.ID) })
	b.Attempts++
	b.State, b.Error, b.StartedAt, b.EndedAt = BatchRunning, "", m.o.Now(), time.Time{}
	if err := m.o.Store.UpdateFeedBatch(ctx, b); err != nil {
		return err
	}
	m.batchChanged(b)
	return m.process(ctx, b, events)
}

// process runs one batch and stores its outcome.
func (m *Manager) process(ctx context.Context, b domain.FeedBatch, raw []domain.FeedEvent) error {
	in, known, err := m.input(ctx, b, raw)
	if err == nil {
		var res runResult
		res, err = m.runBatch(ctx, in)
		if err == nil {
			b.Agent, b.Model = res.Agent, res.Model
			m.store(ctx, b, in, known, res.Reply)
			b.State, b.Summary = BatchDone, cutBytes(oneLine(res.Reply.Summary), MaxActionText)
		}
	}
	if err != nil {
		b.State, b.Error = BatchFailed, cutBytes(err.Error(), MaxActionText)
	}
	b.EndedAt = m.o.Now()
	if uerr := m.o.Store.UpdateFeedBatch(context.WithoutCancel(ctx), b); uerr != nil && err == nil {
		err = uerr
	}
	m.batchChanged(b)
	if err != nil {
		return fmt.Errorf("batch %d attempt %d: %w", b.ID, b.Attempts, err)
	}
	return nil
}

// batchKnown is what a batch's run could see: its events (raw and folded) and
// the open threads.
type batchKnown struct {
	batch   map[int64]domain.FeedEvent // answerable: batch events, digests and their members
	threads map[string][]domain.FeedEvent
	all     map[int64]domain.FeedEvent // citable: batch plus open threads
}

// input builds the batch's files: digests folded and stored, open threads,
// session facts, memory, decisions and summaries.
func (m *Manager) input(ctx context.Context, b domain.FeedBatch, raw []domain.FeedEvent) (BatchInput, batchKnown, error) {
	known := batchKnown{batch: map[int64]domain.FeedEvent{}, threads: map[string][]domain.FeedEvent{}, all: map[int64]domain.FeedEvent{}}
	for _, e := range raw {
		known.batch[e.ID] = e
	}
	folded := FoldDigests(raw)
	for i, e := range folded {
		if e.Kind != DigestKind {
			continue
		}
		if m.o.Digests != nil {
			stored, err := m.o.Digests.SaveFeedDigest(ctx, digestMembers(raw, e.Channel), e)
			if err != nil {
				return BatchInput{}, known, fmt.Errorf("store a digest: %w", err)
			}
			e = stored
		} else {
			e.ID = -b.ID*1000 - int64(i) - 1
		}
		folded[i] = e
		known.batch[e.ID] = e
	}
	for _, e := range folded {
		if e.Kind == DigestKind {
			continue
		}
		if _, ok := known.threads[e.ThreadKey]; ok {
			continue
		}
		earlier, err := m.o.Events.Thread(ctx, e.ThreadKey, firstInThread(raw, e.ThreadKey), maxThreadEvents)
		if err != nil {
			return BatchInput{}, known, err
		}
		known.threads[e.ThreadKey] = earlier
	}
	for id, e := range known.batch {
		known.all[id] = e
	}
	for _, evs := range known.threads {
		for _, e := range evs {
			known.all[e.ID] = e
		}
	}
	in := BatchInput{Batch: b, Events: folded, Threads: known.threads, Nonce: newNonce(), Now: m.o.Now()}
	var err error
	if m.o.Context != nil {
		if in.Context, err = m.o.Context(ctx); err != nil {
			m.o.Log("feed: session context: %v", err)
		}
	}
	if in.Memory, err = m.memory.Bullets(); err != nil {
		return in, known, fmt.Errorf("read memory: %w", err)
	}
	if in.Decisions, err = m.Decisions(ctx); err != nil {
		return in, known, err
	}
	if in.Summaries, err = m.summaries(ctx, b.ID); err != nil {
		return in, known, err
	}
	return in, known, nil
}

func firstInThread(events []domain.FeedEvent, key string) int64 {
	for _, e := range events {
		if e.ThreadKey == key {
			return e.ID
		}
	}
	return 0
}

// Decisions are the operator's answers to the newest 50 decided actions,
// oldest first.
func (m *Manager) Decisions(ctx context.Context) ([]Decision, error) {
	acts, err := m.o.Store.ListFeedActions(ctx, []domain.FeedActionState{ActionApproved, ActionRunning, ActionDone, ActionFailed, ActionDismissed, ActionExpired}, listAll)
	if err != nil {
		return nil, err
	}
	var out []Decision
	for _, a := range acts {
		out = append(out, decisionOf(a))
	}
	slices.SortFunc(out, func(a, b Decision) int { return a.At.Compare(b.At) })
	if len(out) > maxDecisions {
		out = out[len(out)-maxDecisions:]
	}
	return out, nil
}

func decisionOf(a domain.FeedAction) Decision {
	d := Decision{ActionID: a.ID, Kind: a.Kind, At: a.DecidedAt}
	if d.At.IsZero() {
		d.At = a.ExpiresAt
	}
	switch a.State {
	case ActionDismissed:
		d.Outcome = "dismissed"
		if a.Dismissal != nil {
			d.Reason, d.Note = a.Dismissal.Reason, a.Dismissal.Note
		}
	case ActionExpired:
		d.Outcome = "expired"
	default:
		d.Outcome = "approved"
		if a.Chosen != nil {
			d.Option = *a.Chosen
			if *a.Chosen >= 0 && *a.Chosen < len(a.Options) && len(a.ChosenParams) > 0 {
				d.Edited = !jsonEqual(a.ChosenParams, a.Options[*a.Chosen].Params)
			}
		}
	}
	return d
}

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return string(a) == string(b)
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return bytes.Equal(xa, ya)
}

func (m *Manager) summaries(ctx context.Context, current int64) ([]string, error) {
	bs, err := m.o.Store.ListFeedBatches(ctx, 20)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, b := range bs {
		if b.ID != current && b.State == BatchDone && b.Summary != "" {
			out = append(out, b.Summary)
			if len(out) == maxSummaries {
				break
			}
		}
	}
	slices.Reverse(out)
	return out, nil
}

// store validates the reply's actions, stores those that stand, and applies
// the memory ops.
func (m *Manager) store(ctx context.Context, b domain.FeedBatch, in BatchInput, known batchKnown, r Reply) {
	open, err := m.o.Store.ListFeedActions(ctx, OpenStates(), listAll)
	if err != nil {
		m.o.Log("feed: batch %d: list open actions: %v", b.ID, err)
		return
	}
	now := m.o.Now()
	for i, ra := range r.Actions {
		a, why := m.validate(ctx, ra, in, known, open)
		if why != "" {
			m.o.Log("feed: batch %d: action %d dropped: %s", b.ID, i, why)
			continue
		}
		a.BatchID, a.State, a.CreatedAt, a.ExpiresAt = b.ID, ActionProposed, now, now.Add(m.o.ActionTTL)
		a.Trace = m.trace(ctx, a, ra, in, known, b)
		stored, err := m.o.Store.InsertFeedAction(ctx, a)
		if err != nil {
			m.o.Log("feed: batch %d: store action: %v", b.ID, err)
			continue
		}
		open = append(open, stored)
		if m.o.OnAction != nil {
			m.o.OnAction(stored)
		}
	}
	if err := writeDigestLines(m.o.Dir, b.ID, r.Digest); err != nil {
		m.o.Log("feed: batch %d: digest lines: %v", b.ID, err)
	}
	if len(r.MemoryOps) > 0 {
		if _, err := m.memory.Apply(r.MemoryOps, now); err != nil {
			m.o.Log("feed: batch %d: memory: %v", b.ID, err)
		}
	}
}

// validate turns a reply action into an action, or says why it is dropped.
func (m *Manager) validate(ctx context.Context, ra ReplyAction, in BatchInput, known batchKnown, open []domain.FeedAction) (domain.FeedAction, string) {
	if !domain.FeedSafeKind(ra.Kind) {
		return domain.FeedAction{}, fmt.Sprintf("kind %q is not a phase 1 kind", ra.Kind)
	}
	if len(ra.Answers) == 0 {
		return domain.FeedAction{}, "answers no event"
	}
	answers := slices.Clone(ra.Answers)
	slices.Sort(answers)
	answers = slices.Compact(answers)
	var answered []domain.FeedEvent
	held := true
	for _, id := range answers {
		e, ok := known.batch[id]
		if !ok {
			return domain.FeedAction{}, fmt.Sprintf("answers event %d, which is not in the batch", id)
		}
		answered = append(answered, e)
		h, err := m.o.Store.FeedThreadHeld(ctx, e.ThreadKey, e.ID)
		if err != nil || !h {
			held = false
		}
	}
	if held {
		return domain.FeedAction{}, "every answered thread is held"
	}
	for _, id := range ra.Cites.Events {
		if _, ok := known.all[id]; !ok {
			return domain.FeedAction{}, fmt.Sprintf("cites event %d, which the batch does not hold", id)
		}
	}
	for _, o := range open {
		if o.Kind == ra.Kind && slices.Equal(sortedIDs(o.Answers), answers) {
			return domain.FeedAction{}, fmt.Sprintf("duplicates open action %d", o.ID)
		}
	}
	var opts []domain.FeedOption
	for _, o := range ra.Options {
		if why := checkOption(o, ra.Kind, answered, in.Context, m.o.Context != nil); why != "" {
			m.o.Log("feed: option %q dropped: %s", oneLine(o.Label), why)
			continue
		}
		o.Label = cutBytes(oneLine(o.Label), 200)
		o.Why = cutBytes(oneLine(o.Why), MaxActionText)
		opts = append(opts, o)
	}
	if len(opts) == 0 {
		return domain.FeedAction{}, "no valid option"
	}
	prio := ra.Priority
	switch prio {
	case "high", "normal", "low":
	default:
		prio = "normal"
	}
	return domain.FeedAction{Kind: ra.Kind, Rationale: cutBytes(strings.TrimSpace(ra.Rationale), MaxActionText),
		Priority: prio, Answers: answers, Options: opts}, ""
}

func sortedIDs(ids []int64) []int64 {
	s := slices.Clone(ids)
	slices.Sort(s)
	return slices.Compact(s)
}

// checkOption checks an option's kind and params.
func checkOption(o domain.FeedOption, action domain.FeedActionKind, answered []domain.FeedEvent, sessions []SessionFacts, checkSessions bool) string {
	kind := o.EffectiveKind(action)
	if !domain.FeedSafeKind(kind) {
		return fmt.Sprintf("kind %q is not a phase 1 kind", kind)
	}
	if len(o.Params) > 2*MaxActionText {
		return "params too large"
	}
	var p struct {
		Channel  string `json:"channel"`
		ThreadTS string `json:"thread_ts"`
		Text     string `json:"text"`
		Session  string `json:"session"`
		URL      string `json:"url"`
		Why      string `json:"why"`
	}
	if err := json.Unmarshal(o.Params, &p); err != nil {
		return "params do not parse"
	}
	if len(p.Text) > MaxActionText || len(p.Why) > MaxActionText {
		return "text over 4 KiB"
	}
	switch kind {
	case domain.FeedSlackDraft:
		if p.Channel == "" || strings.TrimSpace(p.Text) == "" {
			return "slack_draft needs a channel and text"
		}
		if !slices.ContainsFunc(answered, func(e domain.FeedEvent) bool { return eventChannel(e) == p.Channel }) {
			return fmt.Sprintf("channel %s is not where an answered event came from", p.Channel)
		}
	case domain.FeedTodoAdd:
		if p.Session == "" || strings.TrimSpace(p.Text) == "" {
			return "todo_add needs a session and text"
		}
		if checkSessions && !slices.ContainsFunc(sessions, func(s SessionFacts) bool { return s.ID == p.Session || s.Name == p.Session }) {
			return fmt.Sprintf("session %q is not a visible session", p.Session)
		}
	case domain.FeedRead:
		u, err := url.Parse(p.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return "read needs an http(s) url"
		}
	}
	return ""
}

// eventChannel is the Slack channel an event came from.
func eventChannel(e domain.FeedEvent) string {
	if e.Channel != "" {
		return e.Channel
	}
	if e.Source == "slack" {
		ch, _, _ := strings.Cut(e.ExternalID, "/")
		return ch
	}
	return ""
}

// trace gathers what BuildTrace needs for one action: every earlier event of
// the answered threads, the cited events, memory bullets and decisions.
func (m *Manager) trace(ctx context.Context, a domain.FeedAction, ra ReplyAction, in BatchInput, known batchKnown, b domain.FeedBatch) []domain.FeedTraceEntry {
	var answered []domain.FeedEvent
	for _, id := range a.Answers {
		answered = append(answered, known.batch[id])
	}
	threads := map[string][]domain.FeedEvent{}
	for _, e := range answered {
		if _, ok := threads[e.ThreadKey]; ok || e.Kind == DigestKind {
			continue
		}
		evs, err := m.o.Events.Thread(ctx, e.ThreadKey, e.ID, causeThreadRead)
		if err != nil {
			evs = known.threads[e.ThreadKey]
		}
		ids := map[int64]bool{}
		for _, x := range evs {
			ids[x.ID] = true
		}
		for _, x := range in.Events {
			if x.ThreadKey == e.ThreadKey && x.ID < e.ID && x.Kind != DigestKind && !ids[x.ID] {
				evs = append(evs, x)
			}
		}
		threads[e.ThreadKey] = evs
	}
	var cited []domain.FeedEvent
	for _, id := range ra.Cites.Events {
		cited = append(cited, known.all[id])
	}
	var mem []MemoryBullet
	for _, b := range in.Memory {
		if slices.Contains(ra.Cites.Memory, b.ID) {
			mem = append(mem, b)
		}
	}
	var decs []Decision
	for _, d := range in.Decisions {
		if slices.Contains(ra.Cites.Decisions, d.ActionID) {
			decs = append(decs, d)
		}
	}
	return BuildTrace(answered, threads, cited, mem, decs, b)
}

// expire turns proposed actions past their time into expired.
func (m *Manager) expire(ctx context.Context) {
	acts, err := m.o.Store.ListFeedActions(ctx, []domain.FeedActionState{ActionProposed}, listAll)
	if err != nil {
		return
	}
	now := m.o.Now()
	for _, a := range acts {
		if a.ExpiresAt.IsZero() || a.ExpiresAt.After(now) {
			continue
		}
		a.State, a.DecidedAt, a.DecidedBy = ActionExpired, now, "aiman"
		if err := m.o.Store.UpdateFeedAction(ctx, a); err == nil && m.o.OnAction != nil {
			m.o.OnAction(a)
		}
	}
}

// publishFloor tells the collector the smallest event id still needed: the
// open actions', a running or retryable batch's, and every event above the
// high-water mark.
func (m *Manager) publishFloor(ctx context.Context) {
	if m.o.SetFloor == nil {
		return
	}
	hw, err := m.o.Store.FeedHighWater(ctx)
	if err != nil {
		return
	}
	floor := hw + 1
	note := func(id int64) {
		if id > 0 && id < floor {
			floor = id
		}
	}
	if f, err := m.o.Store.OpenFeedActionEventFloor(ctx); err == nil {
		note(f)
	} else {
		return
	}
	if bs, err := m.o.Store.ListFeedBatches(ctx, 1); err == nil {
		for _, b := range bs {
			if b.State == BatchRunning || (b.State == BatchFailed && b.Attempts < maxAttempts) {
				for _, id := range b.EventIDs {
					note(id)
				}
			}
		}
	}
	if err := m.o.SetFloor(ctx, floor); err != nil {
		m.o.Log("feed: set the prune floor: %v", err)
	}
}

func (m *Manager) batchChanged(b domain.FeedBatch) {
	if m.o.OnBatch != nil {
		m.o.OnBatch(b)
	}
}
