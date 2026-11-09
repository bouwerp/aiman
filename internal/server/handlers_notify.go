package server

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/notify"
	"github.com/bouwerp/aiman/internal/pane"
)

// cachedState is the stateSweep's latest answer for a session, spelled as the
// event stream sends it. Asking and Reason stay out of it, so they cannot make a
// session_activity event differ.
type cachedState struct {
	State      string
	Confidence string
	// At is when the sample that produced it started.
	At time.Time
}

// cachedSessionState reads the state the sweep last stored for a session.
func (s *Server) cachedSessionState(id string) (cachedState, bool) {
	v, ok := s.sessionStates.Load(id)
	if !ok {
		return cachedState{}, false
	}
	st, ok := v.(cachedState)
	return st, ok
}

// notifyReason maps a screen rule's match to the fixed text a notification may
// carry. An unrecognised reason is still a fixed string, never screen text.
func notifyReason(paneReason string) string {
	switch paneReason {
	case pane.MenuReason:
		return notify.ReasonMenuOpen
	case pane.QuestionReason:
		return notify.ReasonAskedQuestion
	default:
		return notify.ReasonConfirmation
	}
}

// observation builds what the recorder is told. The hook's message belongs only
// to a report that said waiting_input: a menu found under a working report has
// its own reason.
func observation(sess domain.Session, sample stateSample) notify.Observation {
	obs := notify.Observation{
		SessionID: sess.ID,
		Label:     notify.Label(sess.IssueKey, sess.Name, sess.ID),
		State:     sample.State,
		Confident: sample.Confidence == pane.High,
		Ended:     sample.live.AgentEnded,
		Reason:    sample.Reason,
	}
	if sample.live.HookState == domain.AgentStateWaitingInput {
		obs.Question = sample.live.HookStateMessage
	}
	return obs
}

// observeState samples a session, caches the answer for the event stream and
// tells the recorder when one is configured. Hidden helpers are not the
// operator's to hear about, so they get neither.
func (s *Server) observeState(ctx context.Context, sess domain.Session) {
	if s.hiddenSession(sess) {
		return
	}
	at := time.Now()
	s.recordSample(ctx, sess, at, s.sampleState(ctx, sess))
}

// recordSample caches a sample taken from at for the event stream and tells
// the recorder.
func (s *Server) recordSample(ctx context.Context, sess domain.Session, at time.Time, sample stateSample) {
	s.sessionStates.Store(sess.ID, cachedState{
		State:      string(sample.State),
		Confidence: confidenceName(sample.Confidence),
		At:         at,
	})
	rec := s.NotifyRecorder()
	if rec == nil {
		return
	}
	obs := observation(sess, sample)
	obs.Asking = s.asksQuestion(sample)
	_ = rec.Observe(ctx, obs)
}

// hiddenSession reports an aiman helper. The holder is asked too, because a
// helper's row can be stored before its role is.
func (s *Server) hiddenSession(sess domain.Session) bool {
	if sess.Role.Hidden() {
		return true
	}
	if s.pty == nil {
		return false
	}
	info, err := s.pty.Get(sess.ID)
	return err == nil && domain.SessionRole(info.Role).Hidden()
}

// asksQuestion reports an idle turn that ended in a question. Only a confident
// idle counts, and an ended agent is not waiting on anyone.
func (s *Server) asksQuestion(sample stateSample) bool {
	if sample.State != domain.AgentStateIdle || sample.Confidence != pane.High || sample.live.AgentEnded {
		return false
	}
	screen := sample.screen
	if screen == "" {
		captured, ok := s.captureLive(sample.live)
		if !ok {
			return false
		}
		screen = captured
	}
	return pane.AsksQuestion(screen)
}

// sweepMemo is what the sweep remembers about a session between passes.
type sweepMemo struct {
	// hook is sweepHookKey at the last pass that saw the session.
	hook string
	// captured is the pass that last captured it; zero is never.
	captured uint64
}

// Capture groups, in the order a pass serves them.
const (
	sweepTransition = iota // the hook moved to ended or waiting_input since the last pass
	sweepStale             // no fresh hook report: the screen rules decide
	sweepWorking           // a fresh working report: a menu shows only on screen
	sweepQuestion          // a fresh idle report while a recorder listens for questions
	sweepGroups
	sweepNoCapture = -1 // the fresh report answers alone
)

// sweepHookKey is the hook state a pass compares with the last one: ended, a
// fresh report's state, or empty for a stale or missing report.
func sweepHookKey(sess domain.Session, now time.Time) string {
	if sess.AgentEnded {
		return "ended"
	}
	if st, ok := agenthook.ResolveHookState(sess, now); ok {
		return string(st)
	}
	return ""
}

// sweepGroup is the capture group of a session whose hook key is key; prev is
// the key the last pass saw, and known says there was a last pass.
func (s *Server) sweepGroup(key, prev string, known bool) int {
	transition := key == "ended" || key == string(domain.AgentStateWaitingInput)
	switch {
	case transition && known && key != prev:
		return sweepTransition
	case key == "":
		return sweepStale
	case key == string(domain.AgentStateWorking):
		return sweepWorking
	case key == string(domain.AgentStateIdle) && s.NotifyRecorder() != nil:
		// asksQuestion captures a confident idle to look for a question.
		return sweepQuestion
	default:
		return sweepNoCapture
	}
}

type sweepItem struct {
	sess domain.Session
	key  string
	memo *sweepMemo
}

// sweepStates runs one pass of the stateSweep, and drops cached state for every
// session that is gone, no longer active or hidden. A call made while another
// pass is still running returns at once and counts as a skipped tick.
//
// A pass captures at most sweepCaptureBudget sessions: transitions, then stale
// or missing hook reports, then fresh working reports, then fresh idle reports
// a recorder checks for a question, least recently captured first inside each
// group. Sessions past the budget are not sampled and keep their cached state.
// A session whose fresh report answers alone is sampled without a capture.
func (s *Server) sweepStates(ctx context.Context) {
	if !s.sweepMu.TryLock() {
		s.sweepSkipped.Add(1)
		return
	}
	defer s.sweepMu.Unlock()
	list, err := s.loadSessions(ctx)
	if err != nil {
		return
	}
	if s.sweepMemos == nil {
		s.sweepMemos = map[string]*sweepMemo{}
	}
	s.sweepPass++
	now := time.Now()
	active := make(map[string]struct{}, len(list))
	var groups [sweepGroups][]sweepItem
	for _, sess := range list {
		if sess.Status != domain.SessionStatusActive || sess.Role.Hidden() {
			continue
		}
		active[sess.ID] = struct{}{}
		if s.hiddenSession(sess) {
			continue
		}
		sess = s.freshHookFields(ctx, sess)
		key := sweepHookKey(sess, now)
		memo, known := s.sweepMemos[sess.ID]
		if !known {
			memo = &sweepMemo{hook: key}
			s.sweepMemos[sess.ID] = memo
		}
		g := s.sweepGroup(key, memo.hook, known)
		if g == sweepNoCapture {
			memo.hook = key
			at := time.Now()
			s.recordSample(ctx, sess, at, s.sampleState(ctx, sess))
			continue
		}
		groups[g] = append(groups[g], sweepItem{sess: sess, key: key, memo: memo})
	}
	budget := sweepCaptureBudget
	for g := range groups {
		items := groups[g]
		sort.SliceStable(items, func(i, j int) bool { return items[i].memo.captured < items[j].memo.captured })
		for _, it := range items {
			if budget == 0 {
				// A transition stays one until a pass samples it.
				if g != sweepTransition {
					it.memo.hook = it.key
				}
				continue
			}
			budget--
			it.memo.hook, it.memo.captured = it.key, s.sweepPass
			at := time.Now()
			sample := s.sampleState(ctx, it.sess)
			if g == sweepTransition {
				s.captureTransition(&sample)
			}
			s.recordSample(ctx, it.sess, at, sample)
		}
	}
	s.sessionStates.Range(func(id, _ any) bool {
		if key, ok := id.(string); ok {
			if _, still := active[key]; !still {
				s.sessionStates.Delete(key)
			}
		}
		return true
	})
	for id := range s.sweepMemos {
		if _, still := active[id]; !still {
			delete(s.sweepMemos, id)
		}
	}
}

// captureTransition reads the screen of a session whose hook just moved to
// ended or waiting_input, which the report alone answers without one. A menu
// behind a waiting_input report gives the notification its reason.
func (s *Server) captureTransition(sample *stateSample) {
	screen, ok := s.captureLive(sample.live)
	if !ok {
		return
	}
	sample.screen = screen
	if sample.State != domain.AgentStateWaitingInput {
		return
	}
	if reason, open := pane.MenuOpen(screen); open {
		sample.Reason = notifyReason(reason)
	}
}

// SweepSkipped returns how many sweep ticks were skipped because the previous
// sweep was still running or ran past its period.
func (s *Server) SweepSkipped() int {
	return int(s.sweepSkipped.Load())
}

// pruneNotifications drops log rows past the retention window.
func (s *Server) pruneNotifications(ctx context.Context) {
	if pr, ok := s.repo.(interface {
		PruneNotifications(context.Context, time.Time) error
	}); ok {
		_ = pr.PruneNotifications(ctx, time.Now().Add(-notifyRetention))
	}
}

// runStateSweep samples active sessions each sweep period, whether or not
// notifications are configured, because the event stream reads its cache. The
// period follows the load average and is re-read every tick.
func (s *Server) runStateSweep(ctx context.Context) {
	if s.NotifyRecorder() != nil {
		s.pruneNotifications(ctx)
	}
	period := currentSweepPeriod()
	s.sweepEvery.Store(int64(period))
	t := time.NewTicker(period)
	defer t.Stop()
	lastPrune := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if time.Since(lastPrune) > 24*time.Hour {
				s.pruneNotifications(ctx)
				lastPrune = time.Now()
			}
			start := time.Now()
			s.sweepStates(ctx)
			s.countOverrun(time.Since(start))
			if next := currentSweepPeriod(); next != period {
				period = next
				s.sweepEvery.Store(int64(period))
				t.Reset(period)
			}
		}
	}
}

// countOverrun adds the ticks a sweep lasting elapsed made the ticker drop:
// time.Ticker drops ticks while its receiver is busy, so they never reach the
// TryLock in sweepStates.
func (s *Server) countOverrun(elapsed time.Duration) {
	period := time.Duration(s.sweepEvery.Load())
	if period <= 0 {
		period = notifySweepEvery
	}
	if elapsed > period {
		s.sweepSkipped.Add(int64(elapsed / period))
	}
}

func (s *Server) handleNotifySince(ctx context.Context, req Request) Response {
	s.notifyMu.RLock()
	log := s.notifyLog
	s.notifyMu.RUnlock()
	if log == nil {
		return errResp(req.ID, CodeInvalidParams, "notifications are not enabled")
	}
	var params struct {
		After int64 `json:"after"`
		Limit int   `json:"limit"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return errResp(req.ID, CodeInvalidParams, "invalid params")
		}
	}
	after := params.After
	limit := params.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	if after < 0 {
		last, err := log.LastNotificationSeq(ctx)
		if err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
		entries, err := log.NotificationsSince(ctx, last, limit)
		if err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
		out := make([]map[string]any, 0, len(entries))
		for _, e := range entries {
			out = append(out, map[string]any{
				"seq":        e.Seq,
				"session_id": e.SessionID,
				"kind":       string(e.Kind),
				"label":      e.Label,
				"host":       e.Host,
				"text":       e.Text,
				"at":         e.At.Format(time.RFC3339),
			})
		}
		return Response{ID: req.ID, Result: map[string]any{
			"type":     "notifications",
			"entries":  out,
			"last_seq": last,
		}}
	}
	entries, err := log.NotificationsSince(ctx, after, limit)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	last, _ := log.LastNotificationSeq(ctx)
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]any{
			"seq":        e.Seq,
			"session_id": e.SessionID,
			"kind":       string(e.Kind),
			"label":      e.Label,
			"host":       e.Host,
			"text":       e.Text,
			"at":         e.At.Format(time.RFC3339),
		})
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type":     "notifications",
		"entries":  out,
		"last_seq": last,
	}}
}
