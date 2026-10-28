package server

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/notify"
)

// observeSession builds an observation for a session.
func (s *Server) observeSession(_ context.Context, sess domain.Session) notify.Observation {
	label := notify.Label(sess.IssueKey, sess.Name, sess.ID)
	obs := notify.Observation{
		SessionID: sess.ID,
		Label:     label,
		Ended:     sess.AgentEnded,
		State:     sess.HookState,
	}
	obs.Question = sess.HookStateMessage
	line := s.lastScreenLine(sess)
	obs.LastLine = func() string { return line }
	return obs
}

// lastScreenLine is the last non-empty line of the session's PTY screen, or
// empty when serve cannot read it.
func (s *Server) lastScreenLine(sess domain.Session) string {
	if sess.IsLegacyTmux() || s.pty == nil {
		return ""
	}
	screen, err := s.pty.CaptureScreen(sess.ID)
	if err != nil {
		return ""
	}
	return lastNonEmptyLine(screen)
}

func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		l = strings.ReplaceAll(l, "\r", "")
		// trim spaces/tabs
		l = strings.TrimSpace(l)
		if l != "" {
			return l
		}
	}
	return ""
}

func (s *Server) notifyObserve(ctx context.Context, sess domain.Session) {
	rec := s.NotifyRecorder()
	if rec == nil {
		return
	}
	_ = rec.Observe(ctx, s.observeSession(ctx, sess))
}

func (s *Server) runNotifySweep(ctx context.Context) {
	if rec := s.NotifyRecorder(); rec != nil {
		if p, ok := interface{}(rec).(interface{ PruneOlderThan(time.Time) error }); ok {
			_ = p.PruneOlderThan(time.Now().Add(-notifyRetention))
		} else if s.repo != nil {
			if pr, ok := s.repo.(interface {
				PruneNotifications(context.Context, time.Time) error
			}); ok {
				_ = pr.PruneNotifications(ctx, time.Now().Add(-notifyRetention))
			}
		}
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
				if rec := s.NotifyRecorder(); rec != nil {
					if p, ok := interface{}(rec).(interface{ PruneOlderThan(time.Time) error }); ok {
						_ = p.PruneOlderThan(time.Now().Add(-notifyRetention))
					} else if s.repo != nil {
						if pr, ok := s.repo.(interface {
							PruneNotifications(context.Context, time.Time) error
						}); ok {
							_ = pr.PruneNotifications(ctx, time.Now().Add(-notifyRetention))
						}
					}
				} else if s.repo != nil {
					if pr, ok := s.repo.(interface {
						PruneNotifications(context.Context, time.Time) error
					}); ok {
						_ = pr.PruneNotifications(ctx, time.Now().Add(-notifyRetention))
					}
				}
				lastPrune = time.Now()
			}
			list, err := s.loadSessions(ctx)
			if err != nil {
				continue
			}
			for _, sess := range list {
				if sess.Status != domain.SessionStatusActive {
					continue
				}
				s.notifyObserve(ctx, sess)
			}
		}
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
