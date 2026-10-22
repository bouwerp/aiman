package server

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/todo"
)

// SteerNote is a message for a session's agent: free text, or the formatted
// summary of someone else's change to its to-do list.
type SteerNote struct {
	SessionID string            `json:"session_id"`
	Author    domain.TodoAuthor `json:"author"`
	Text      string            `json:"text"`
}

// SteerStatus is a session's steering queue as callers see it.
type SteerStatus struct {
	Queued          int       `json:"queued"`
	LastDeliveredAt time.Time `json:"last_delivered_at,omitzero"`
	LastError       string    `json:"last_error,omitempty"`
	// Held is why the last delivery attempt waited: working, dialog, ended or
	// unknown. Empty when nothing is held.
	Held string `json:"held,omitempty"`
	// Pending is the undelivered notes, oldest first.
	Pending []SteerNote `json:"pending,omitempty"`
}

// Steerer queues notes for agents and reports on the queue.
type Steerer interface {
	Enqueue(n SteerNote)
	Status(sessionID string) SteerStatus
}

// SteerStore persists the steering queue so it survives a serve restart.
type SteerStore interface {
	AddSteerNote(ctx context.Context, n *domain.SteerRecord) error
	ListUndeliveredSteerNotes(ctx context.Context) ([]domain.SteerRecord, error)
	MarkSteerNotesDelivered(ctx context.Context, ids []int64, at time.Time) error
	DeleteSteerNotes(ctx context.Context, sessionID string) error
	LastSteerDelivery(ctx context.Context) (map[string]time.Time, error)
}

// steerTarget is the session side of steering: whether the agent can take a
// note now (and if not, why), typing the note in, and the current list.
type steerTarget interface {
	steerReady(ctx context.Context, id string, lastDelivered time.Time) (bool, string)
	deliverSteer(ctx context.Context, id, text string) error
	todoList(id string) (domain.TodoList, error)
}

// steerOpTimeout bounds each store or target call so a hung session or
// database cannot stall the delivery loop or a socket handler.
const steerOpTimeout = 20 * time.Second

// queuedNote is an undelivered note. seq identifies it in memory because a
// note whose AddSteerNote failed has no store ID but is still delivered.
type queuedNote struct {
	seq uint64
	rec domain.SteerRecord
}

type steerSession struct {
	// Coalescing buffer, flushed when the window timer fires.
	ops        []todo.Op
	opAuthors  []domain.TodoAuthor
	texts      []string
	textAuthor domain.TodoAuthor
	timer      *time.Timer
	// flushMu keeps a session's flushes in order without holding steerer.mu
	// across store and target calls.
	flushMu sync.Mutex

	pending       []queuedNote
	lastDelivered time.Time
	lastError     string
	held          string
	forgotten     bool
}

type steerer struct {
	store   SteerStore
	target  steerTarget
	publish func(SessionEvent)
	window  time.Duration
	tick    time.Duration
	now     func() time.Time

	kick chan struct{}

	mu       sync.Mutex
	sessions map[string]*steerSession
	seq      uint64
}

var _ Steerer = (*steerer)(nil)

func newSteerer(store SteerStore, target steerTarget, publish func(SessionEvent), window time.Duration) *steerer {
	if publish == nil {
		publish = func(SessionEvent) {}
	}
	return &steerer{
		store:    store,
		target:   target,
		publish:  publish,
		window:   window,
		tick:     time.Second,
		now:      time.Now,
		kick:     make(chan struct{}, 1),
		sessions: map[string]*steerSession{},
	}
}

// sessionLocked returns the state for id, creating it. Callers hold s.mu.
func (s *steerer) sessionLocked(id string) *steerSession {
	st := s.sessions[id]
	if st == nil {
		st = &steerSession{}
		s.sessions[id] = st
	}
	return st
}

// Enqueue buffers a free-text note. Notes from the agent itself are dropped.
func (s *steerer) Enqueue(n SteerNote) {
	if n.SessionID == "" || n.Author == domain.AuthorAgent || strings.TrimSpace(n.Text) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.sessionLocked(n.SessionID)
	if len(st.texts) == 0 {
		st.textAuthor = n.Author
	}
	st.texts = append(st.texts, n.Text)
	s.armLocked(n.SessionID, st)
}

// EnqueueTodo buffers a to-do change for the agent. Changes the agent made
// and changes that only touch Ref links are dropped.
func (s *steerer) EnqueueTodo(sessionID string, author domain.TodoAuthor, ops []todo.Op) {
	if sessionID == "" || !(todo.Change{Author: author, Ops: ops}).SteersAgent() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.sessionLocked(sessionID)
	st.ops = append(st.ops, ops...)
	if !slices.Contains(st.opAuthors, author) {
		st.opAuthors = append(st.opAuthors, author)
	}
	s.armLocked(sessionID, st)
}

// armLocked opens the coalescing window on the first buffered item.
func (s *steerer) armLocked(id string, st *steerSession) {
	if st.timer != nil {
		return
	}
	st.timer = time.AfterFunc(s.window, func() { s.flush(id, st) })
}

// flush turns the buffered window into at most two persisted notes: one for
// the to-do ops and one for the joined free texts.
func (s *steerer) flush(id string, st *steerSession) {
	st.flushMu.Lock()
	defer st.flushMu.Unlock()

	s.mu.Lock()
	if st.forgotten {
		s.mu.Unlock()
		return
	}
	ops, authors := st.ops, st.opAuthors
	texts, textAuthor := st.texts, st.textAuthor
	st.ops, st.opAuthors, st.texts, st.textAuthor, st.timer = nil, nil, nil, "", nil
	s.mu.Unlock()

	now := s.now()
	var recs []domain.SteerRecord
	var errs []error
	if len(ops) > 0 {
		list, err := s.target.todoList(id)
		if err != nil {
			errs = append(errs, err)
		}
		recs = append(recs, domain.SteerRecord{SessionID: id, Author: authors[0], Text: todo.FormatSteerNote(authors, ops, list), CreatedAt: now})
	}
	if len(texts) > 0 {
		recs = append(recs, domain.SteerRecord{SessionID: id, Author: textAuthor, Text: strings.Join(texts, "\n\n"), CreatedAt: now})
	}
	if len(recs) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), steerOpTimeout)
	defer cancel()
	for i := range recs {
		if err := s.store.AddSteerNote(ctx, &recs[i]); err != nil {
			errs = append(errs, err)
		}
	}

	s.mu.Lock()
	if st.forgotten {
		s.mu.Unlock()
		// Forget ran while these were being written; remove them again.
		_ = s.store.DeleteSteerNotes(ctx, id)
		return
	}
	for _, r := range recs {
		s.seq++
		st.pending = append(st.pending, queuedNote{seq: s.seq, rec: r})
	}
	if len(errs) > 0 {
		st.lastError = errors.Join(errs...).Error()
	}
	queued := len(st.pending)
	s.mu.Unlock()

	s.publish(SessionEvent{Type: "steer", ID: id, Queued: &queued})
	s.Kick(id)
}

// Status reports a session's queue. An unknown session has an empty status.
func (s *steerer) Status(sessionID string) SteerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.sessions[sessionID]
	if st == nil {
		return SteerStatus{}
	}
	out := SteerStatus{
		Queued:          len(st.pending),
		LastDeliveredAt: st.lastDelivered,
		LastError:       st.lastError,
		Held:            st.held,
	}
	for _, q := range st.pending {
		out.Pending = append(out.Pending, SteerNote{SessionID: sessionID, Author: q.rec.Author, Text: q.rec.Text})
	}
	return out
}

// Kick wakes the delivery loop, for example after a session reported idle.
// The loop checks every session, so the id only documents the cause.
func (s *steerer) Kick(string) {
	if s == nil {
		return
	}
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Forget drops a session's buffered and queued notes, in memory and in the
// store.
func (s *steerer) Forget(sessionID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if st := s.sessions[sessionID]; st != nil {
		st.forgotten = true
		if st.timer != nil {
			st.timer.Stop()
		}
		delete(s.sessions, sessionID)
	}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), steerOpTimeout)
	defer cancel()
	_ = s.store.DeleteSteerNotes(ctx, sessionID)
}

// Load restores undelivered notes and each session's last delivery time from
// the store, so a restart neither loses nor repeats notes and the to-do merge
// keeps its delivery guard. Notes already in memory are not added twice.
func (s *steerer) Load(ctx context.Context) error {
	recs, err := s.store.ListUndeliveredSteerNotes(ctx)
	if err != nil {
		return err
	}
	last, err := s.store.LastSteerDelivery(ctx)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range recs {
		st := s.sessionLocked(r.SessionID)
		if slices.ContainsFunc(st.pending, func(q queuedNote) bool { return q.rec.ID == r.ID }) {
			continue
		}
		s.seq++
		st.pending = append(st.pending, queuedNote{seq: s.seq, rec: r})
	}
	for id, at := range last {
		st := s.sessionLocked(id)
		if at.After(st.lastDelivered) {
			st.lastDelivered = at
		}
	}
	return nil
}

// Run delivers queued notes until ctx is done, waking on Kick and on a tick.
func (s *steerer) Run(ctx context.Context) {
	t := time.NewTicker(s.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
		}
		s.deliverAll(ctx)
	}
}

func (s *steerer) deliverAll(ctx context.Context) {
	s.mu.Lock()
	var ids []string
	for id, st := range s.sessions {
		if len(st.pending) > 0 {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	slices.Sort(ids)
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		s.deliver(ctx, id)
	}
}

// deliver types every undelivered note for one session as a single message
// once the target reports the agent ready.
func (s *steerer) deliver(ctx context.Context, id string) {
	s.mu.Lock()
	st := s.sessions[id]
	if st == nil || len(st.pending) == 0 {
		s.mu.Unlock()
		return
	}
	batch := slices.Clone(st.pending)
	lastDelivered := st.lastDelivered
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, steerOpTimeout)
	defer cancel()

	ready, held := s.target.steerReady(ctx, id, lastDelivered)
	if !ready {
		s.mu.Lock()
		st.held = held
		s.mu.Unlock()
		return
	}

	texts := make([]string, 0, len(batch))
	var ids []int64
	for _, q := range batch {
		texts = append(texts, q.rec.Text)
		if q.rec.ID != 0 {
			ids = append(ids, q.rec.ID)
		}
	}
	if err := s.target.deliverSteer(ctx, id, strings.Join(texts, "\n\n")); err != nil {
		s.mu.Lock()
		st.held = ""
		st.lastError = err.Error()
		s.mu.Unlock()
		return
	}
	at := s.now()
	markErr := s.store.MarkSteerNotesDelivered(ctx, ids, at)

	s.mu.Lock()
	if st.forgotten {
		s.mu.Unlock()
		return
	}
	st.pending = slices.DeleteFunc(st.pending, func(q queuedNote) bool {
		return slices.ContainsFunc(batch, func(b queuedNote) bool { return b.seq == q.seq })
	})
	st.lastDelivered = at
	st.held = ""
	st.lastError = ""
	if markErr != nil {
		st.lastError = markErr.Error()
	}
	queued := len(st.pending)
	s.mu.Unlock()

	s.publish(SessionEvent{Type: "steer", ID: id, Queued: &queued, DeliveredAt: at.Format(time.RFC3339Nano)})
}
