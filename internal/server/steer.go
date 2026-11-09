package server

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/todo"
)

// SteerNote is a message for a session's agent: free text, or the formatted
// summary of someone else's change to its to-do list.
type SteerNote struct {
	SessionID string            `json:"session_id"`
	Author    domain.TodoAuthor `json:"author"`
	// From is the sending session of a peer note; empty otherwise.
	From string `json:"from,omitempty"`
	Text string `json:"text"`
}

// Peer note limits: the payload size, and the undelivered peer notes a target
// may hold from one sender and from everyone.
const (
	PeerNoteMaxBytes   = 4096
	PeerNotesPerPair   = 8
	PeerNotesPerTarget = 16
)

// ErrPeerQueueFull refuses a peer note over PeerNotesPerPair or
// PeerNotesPerTarget.
var ErrPeerQueueFull = errors.New("peer queue full")

// errSteerForgotten refuses a peer note for a session forgotten meanwhile.
var errSteerForgotten = errors.New("session was forgotten")

// peerDeliveredKeep is how long Delivered remembers a delivered peer note.
const peerDeliveredKeep = time.Hour

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
	// EnqueuePeer persists a peer note at once and returns its row id.
	EnqueuePeer(n SteerNote) (int64, error)
	// Delivered reports when a persisted peer note was typed.
	Delivered(id int64) (time.Time, bool)
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
	// deliverSteer returns when the note was submitted (its first Return),
	// which the gate compares agent reports against.
	deliverSteer(ctx context.Context, id, text string) (time.Time, error)
	todoList(id string) (domain.TodoList, error)
}

// steerOpTimeout bounds each store or target call so a hung session or
// database cannot stall the delivery loop or a socket handler. A variable so
// tests can shorten it; production stays at 20s, under the 30s socket cap.
var steerOpTimeout = 20 * time.Second

// queuedNote is an undelivered note. seq identifies it in memory because a
// note whose AddSteerNote failed has no store ID but is still delivered.
type queuedNote struct {
	seq uint64
	rec domain.SteerRecord
	// submittedAt is set once the note has been typed and Return pressed, but
	// the store mark failed. A later pass retries the mark and does not type
	// the note again.
	submittedAt time.Time
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
	// running is set while Run's loop runs; without it Kick runs one pass.
	running atomic.Bool
	// deliverMu keeps delivery passes from overlapping, so a note is never
	// typed twice.
	deliverMu sync.Mutex

	mu       sync.Mutex
	sessions map[string]*steerSession
	seq      uint64
	// delivered holds the submit time of delivered peer notes by row id, for
	// Delivered; entries older than peerDeliveredKeep are dropped.
	delivered map[int64]time.Time
}

var _ Steerer = (*steerer)(nil)

func newSteerer(store SteerStore, target steerTarget, publish func(SessionEvent), window time.Duration) *steerer {
	if publish == nil {
		publish = func(SessionEvent) {}
	}
	return &steerer{
		store:     store,
		target:    target,
		publish:   publish,
		window:    window,
		tick:      time.Second,
		now:       time.Now,
		kick:      make(chan struct{}, 1),
		sessions:  map[string]*steerSession{},
		delivered: map[int64]time.Time{},
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
	if n.SessionID == "" || n.Author == domain.AuthorAgent || n.Author == domain.AuthorPeer || strings.TrimSpace(n.Text) == "" {
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

// EnqueuePeer persists a peer note as its own record at once, outside the
// coalescing window, and queues it for delivery. It never joins another note.
// The session's flush lock keeps the cap check and the insert together.
func (s *steerer) EnqueuePeer(n SteerNote) (int64, error) {
	if n.SessionID == "" || n.From == "" || strings.TrimSpace(n.Text) == "" {
		return 0, errors.New("peer note needs a target, a sender and text")
	}
	s.mu.Lock()
	st := s.sessionLocked(n.SessionID)
	s.mu.Unlock()

	st.flushMu.Lock()
	defer st.flushMu.Unlock()

	s.mu.Lock()
	err := peerCapErr(st, n.From)
	s.mu.Unlock()
	if err != nil {
		return 0, err
	}

	rec := domain.SteerRecord{SessionID: n.SessionID, Author: domain.AuthorPeer, From: n.From, Text: n.Text, CreatedAt: s.now()}
	ctx, cancel := context.WithTimeout(context.Background(), steerOpTimeout)
	defer cancel()
	if err := s.store.AddSteerNote(ctx, &rec); err != nil {
		return 0, err
	}

	s.mu.Lock()
	if st.forgotten {
		s.mu.Unlock()
		_ = s.store.DeleteSteerNotes(ctx, n.SessionID)
		return 0, errSteerForgotten
	}
	s.seq++
	st.pending = append(st.pending, queuedNote{seq: s.seq, rec: rec})
	queued := len(st.pending)
	s.mu.Unlock()

	s.publish(SessionEvent{Type: "steer", ID: n.SessionID, Queued: &queued})
	s.Kick(n.SessionID)
	return rec.ID, nil
}

// peerCapErr counts the session's undelivered peer notes. Callers hold s.mu.
func peerCapErr(st *steerSession, from string) error {
	var all, pair int
	for _, q := range st.pending {
		if q.rec.Author != domain.AuthorPeer {
			continue
		}
		all++
		if q.rec.From == from {
			pair++
		}
	}
	if pair >= PeerNotesPerPair || all >= PeerNotesPerTarget {
		return ErrPeerQueueFull
	}
	return nil
}

// Delivered reports when the persisted note id was typed, for peer notes
// delivered in the last hour.
func (s *steerer) Delivered(id int64) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.delivered[id]
	return at, ok
}

// EnqueueTodo buffers a to-do change for the agent. Changes the agent made,
// peer authors and changes that only touch Ref links are dropped.
func (s *steerer) EnqueueTodo(sessionID string, author domain.TodoAuthor, ops []todo.Op) {
	if sessionID == "" || author == domain.AuthorPeer || !(todo.Change{Author: author, Ops: ops}).SteersAgent() {
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
		out.Pending = append(out.Pending, SteerNote{SessionID: sessionID, Author: q.rec.Author, From: q.rec.From, Text: q.rec.Text})
	}
	return out
}

// Kick wakes the delivery loop, for example after a session reported idle.
// The loop checks every session, so the id only documents the cause. Before
// Run starts, Kick runs one delivery pass of its own.
func (s *steerer) Kick(string) {
	if s == nil {
		return
	}
	if !s.running.Load() {
		go s.deliverAll(context.Background())
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
	s.running.Store(true)
	defer s.running.Store(false)
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
	s.deliverMu.Lock()
	defer s.deliverMu.Unlock()
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

// deliver types the next batch for one session as a single message once the
// target reports the agent ready.
func (s *steerer) deliver(ctx context.Context, id string) {
	s.mu.Lock()
	st := s.sessions[id]
	if st == nil || len(st.pending) == 0 {
		s.mu.Unlock()
		return
	}
	batch := nextBatch(st.pending)
	lastDelivered := st.lastDelivered
	s.mu.Unlock()

	// A note already typed stays queued until the mark lands. Retry that mark
	// before typing anything else, and do not type the note again.
	if sent, at, ok := submittedBatch(batch); ok {
		s.finishDelivery(ctx, id, sent, at)
		return
	}

	// Readiness and submit each get the full budget. One shared context let a
	// slow gate cancel the submit, or a slow submit cancel the mark.
	readyCtx, readyCancel := context.WithTimeout(ctx, steerOpTimeout)
	ready, held := s.target.steerReady(readyCtx, id, lastDelivered)
	readyCancel()
	if !ready {
		s.mu.Lock()
		if st = s.sessions[id]; st != nil {
			st.held = held
		}
		s.mu.Unlock()
		return
	}

	deliverCtx, deliverCancel := context.WithTimeout(ctx, steerOpTimeout)
	at, err := s.target.deliverSteer(deliverCtx, id, joinSteerText(batch))
	deliverCancel()
	if err != nil {
		s.mu.Lock()
		if st = s.sessions[id]; st != nil && !st.forgotten {
			st.held = ""
			st.lastError = err.Error()
		}
		s.mu.Unlock()
		return
	}
	s.finishDelivery(ctx, id, batch, at)
}

// nextBatch is the prefix of pending typed in one submit: a peer note alone,
// or the run of other notes before the first peer note. A peer note is never
// joined with other text, so its label stays the first thing typed.
func nextBatch(pending []queuedNote) []queuedNote {
	if pending[0].rec.Author == domain.AuthorPeer {
		return slices.Clone(pending[:1])
	}
	end := slices.IndexFunc(pending, func(q queuedNote) bool { return q.rec.Author == domain.AuthorPeer })
	if end < 0 {
		end = len(pending)
	}
	return slices.Clone(pending[:end])
}

func submittedBatch(batch []queuedNote) ([]queuedNote, time.Time, bool) {
	var sent []queuedNote
	var at time.Time
	for _, q := range batch {
		if q.submittedAt.IsZero() {
			continue
		}
		sent = append(sent, q)
		if q.submittedAt.After(at) {
			at = q.submittedAt
		}
	}
	return sent, at, len(sent) > 0
}

func joinSteerText(batch []queuedNote) string {
	texts := make([]string, 0, len(batch))
	for _, q := range batch {
		texts = append(texts, q.rec.Text)
	}
	return strings.Join(texts, "\n\n")
}

func noteIDs(batch []queuedNote) []int64 {
	var ids []int64
	for _, q := range batch {
		if q.rec.ID != 0 {
			ids = append(ids, q.rec.ID)
		}
	}
	return ids
}

func batchHasSeq(batch []queuedNote, seq uint64) bool {
	for _, b := range batch {
		if b.seq == seq {
			return true
		}
	}
	return false
}

// finishDelivery persists the submit. The mark uses its own context, so a
// deadline that fired while typing does not fail it. A failed mark leaves the
// notes queued and does not publish them as delivered.
func (s *steerer) finishDelivery(ctx context.Context, id string, batch []queuedNote, at time.Time) {
	if at.IsZero() {
		at = s.now()
	}
	markCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), steerOpTimeout)
	defer cancel()
	markErr := s.store.MarkSteerNotesDelivered(markCtx, noteIDs(batch), at)

	s.mu.Lock()
	st := s.sessions[id]
	if st == nil || st.forgotten {
		s.mu.Unlock()
		return
	}
	if markErr != nil {
		for i := range st.pending {
			if batchHasSeq(batch, st.pending[i].seq) {
				st.pending[i].submittedAt = at
			}
		}
		st.held = ""
		st.lastError = markErr.Error()
		s.mu.Unlock()
		return
	}
	st.pending = slices.DeleteFunc(st.pending, func(q queuedNote) bool {
		return batchHasSeq(batch, q.seq)
	})
	st.lastDelivered = at
	st.held = ""
	st.lastError = ""
	queued := len(st.pending)
	s.rememberDeliveredLocked(batch, at)
	s.mu.Unlock()

	s.publish(SessionEvent{Type: "steer", ID: id, Queued: &queued, DeliveredAt: at.Format(time.RFC3339Nano)})
	if queued > 0 {
		// The next batch still passes the gate, which waits for a report
		// newer than this delivery.
		s.Kick(id)
	}
}

// rememberDeliveredLocked records the submit time of delivered peer notes for
// Delivered. Callers hold s.mu.
func (s *steerer) rememberDeliveredLocked(batch []queuedNote, at time.Time) {
	cutoff := s.now().Add(-peerDeliveredKeep)
	for id, t := range s.delivered {
		if t.Before(cutoff) {
			delete(s.delivered, id)
		}
	}
	for _, q := range batch {
		if q.rec.Author == domain.AuthorPeer && q.rec.ID != 0 {
			s.delivered[q.rec.ID] = at
		}
	}
}
