package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bouwerp/aiman/internal/domain"
)

// sessionEventLister is the ledger read surface. The sqlite repository
// implements it; a test server can supply any repository that does.
type sessionEventLister interface {
	ListSessionEvents(ctx context.Context, sessionID string, since int64, limit int) ([]domain.SessionEvent, error)
}

// recentEventLister reads a session's newest events, oldest of them first.
type recentEventLister interface {
	ListRecentSessionEvents(ctx context.Context, sessionID string, limit int) ([]domain.SessionEvent, error)
}

type sessionLogCall struct {
	Session  string `json:"session"`
	SinceSeq int64  `json:"since_seq"`
	Limit    int    `json:"limit"`
	// Tail reads the newest Tail events instead of paging from since_seq.
	Tail int `json:"tail"`
}

func (s *Server) handleSessionLog(ctx context.Context, req Request) Response {
	var p sessionLogCall
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, "invalid params")
		}
	}
	if p.Session == "" {
		return errResp(req.ID, CodeInvalidParams, "session is required")
	}
	if p.Tail < 0 || (p.Tail > 0 && (p.SinceSeq != 0 || p.Limit != 0)) {
		return errResp(req.ID, CodeInvalidParams, "tail is a positive count and goes without since_seq and limit")
	}
	sessions, err := s.loadSessions(ctx)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	sess, ok := resolveSession(sessions, p.Session)
	if !ok {
		return errResp(req.ID, CodeNotFound, "session not found")
	}
	var events []domain.SessionEvent
	if p.Tail > 0 {
		lister, ok := s.repo.(recentEventLister)
		if !ok {
			return errResp(req.ID, CodeInvalidParams, "session ledger is not available")
		}
		events, err = lister.ListRecentSessionEvents(ctx, sess.ID, p.Tail)
	} else {
		lister, ok := s.repo.(sessionEventLister)
		if !ok {
			return errResp(req.ID, CodeInvalidParams, "session ledger is not available")
		}
		events, err = lister.ListSessionEvents(ctx, sess.ID, p.SinceSeq, p.Limit)
	}
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, fmt.Sprintf("listing session events: %v", err))
	}
	if events == nil {
		events = []domain.SessionEvent{}
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type":    "session_log",
		"session": sess.ID,
		"events":  events,
	}}
}
