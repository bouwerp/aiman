package server

import (
	"context"
	"encoding/json"

	"github.com/bouwerp/aiman/internal/usecase"
)

type sessionReplayCall struct {
	Session string `json:"session"`
	Out     string `json:"out"`
}

func (s *Server) handleSessionReplay(ctx context.Context, req Request) Response {
	var p sessionReplayCall
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, "invalid params")
		}
	}
	if p.Session == "" {
		return errResp(req.ID, CodeInvalidParams, "session is required")
	}
	if p.Out == "" {
		return errResp(req.ID, CodeInvalidParams, "out is required")
	}
	sessions, err := s.loadSessions(ctx)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	sess, ok := resolveSession(sessions, p.Session)
	if !ok {
		return errResp(req.ID, CodeNotFound, "session not found")
	}
	bundle, err := usecase.ReplayBundle(ctx, s.repo, sess.ID, p.Out)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type":    "session_replay",
		"session": sess.ID,
		"bundle":  bundle,
	}}
}
