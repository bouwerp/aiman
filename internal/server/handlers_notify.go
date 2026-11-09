package server

import (
	"context"
	"encoding/json"
	"time"

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
	sample := s.sampleState(ctx, sess)
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

// sweepStates runs one pass of the stateSweep, and drops cached state for every
// session that is gone, no longer active or hidden. A call made while another
// pass is still running returns at once and counts as a skipped tick.
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
	active := make(map[string]struct{}, len(list))
	for _, sess := range list {
		if sess.Status != domain.SessionStatusActive || sess.Role.Hidden() {
			continue
		}
		active[sess.ID] = struct{}{}
		s.observeState(ctx, sess)
	}
	s.sessionStates.Range(func(id, _ any) bool {
		if key, ok := id.(string); ok {
			if _, still := active[key]; !still {
				s.sessionStates.Delete(key)
			}
		}
		return true
	})
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

// runStateSweep samples every active session each notifySweepEvery, whether or
// not notifications are configured, because the event stream reads its cache.
func (s *Server) runStateSweep(ctx context.Context) {
	if s.NotifyRecorder() != nil {
		s.pruneNotifications(ctx)
	}
	t := time.NewTicker(notifySweepEvery)
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
		}
	}
}

// countOverrun adds the ticks a sweep lasting elapsed made the ticker drop:
// time.Ticker drops ticks while its receiver is busy, so they never reach the
// TryLock in sweepStates.
func (s *Server) countOverrun(elapsed time.Duration) {
	if elapsed > notifySweepEvery {
		s.sweepSkipped.Add(int64(elapsed / notifySweepEvery))
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
