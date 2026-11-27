package server

import (
	"context"
	"encoding/json"
)

// pinSetter is implemented by repositories that store the session pin. Save
// never writes it, so a discovery save cannot clear it.
type pinSetter interface {
	SetPinned(ctx context.Context, id string, pinned bool) error
}

// handlePin sets or clears a session's pin. Pinning is the operator's view of
// the host, so a session caller gets needs_operator.
func (s *Server) handlePin(ctx context.Context, req Request) Response {
	if s.isSessionCall(ctx, req) {
		return errResp(req.ID, CodeNeedsOperator, "only the operator may pin a session")
	}
	var params struct {
		Pinned *bool `json:"pinned"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Pinned == nil {
		return errResp(req.ID, CodeInvalidParams, "pinned is required")
	}
	sess, fail, ok := s.resolveReq(ctx, req)
	if !ok {
		return fail
	}
	ps, ok := s.repo.(pinSetter)
	if s.repo == nil || !ok {
		return errResp(req.ID, CodeUnavailable, "this host cannot store pins")
	}
	// A running session with no row yet gets one, so the pin has a row to land on.
	if err := s.repo.Save(ctx, &sess); err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	if err := ps.SetPinned(ctx, sess.ID, *params.Pinned); err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	sess.Pinned = *params.Pinned
	info := sessionInfo(sess, req.Caller)
	return Response{ID: req.ID, Result: map[string]any{"type": "session", "session": info}}
}
