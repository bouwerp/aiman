package server

import (
	"context"
	"encoding/json"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/limits"
)

// SetLimits gives serve its limits manager.
func (s *Server) SetLimits(m *limits.Manager) { s.limits = m }

// dispatchUsage routes the two reads about what agents have spent and have left.
func (s *Server) dispatchUsage(ctx context.Context, req Request) Response {
	if req.Method == "limits.get" {
		return s.handleLimitsGet(ctx, req)
	}
	return s.handleUsageGet(ctx, req)
}

// sessionLimits is the reading of the provider behind a session's agent.
func (s *Server) sessionLimits(sess domain.Session) *limits.Reading {
	if s.limits == nil {
		return nil
	}
	model := sess.LaunchModel
	if model == "" {
		model = sess.AgentModel
	}
	reading, ok := s.limits.Reading(limits.ProviderFor(sess.AgentName, model))
	if !ok {
		return nil
	}
	return &reading
}

// handleLimitsGet answers from the cache and never waits on a provider: the
// manager runs whatever is due in the background, and a client polls while
// refreshing is true.
func (s *Server) handleLimitsGet(_ context.Context, req Request) Response {
	if s.limits == nil {
		return errResp(req.ID, CodeUnavailable, "limits are not running on this host")
	}
	var p struct {
		Refresh bool `json:"refresh"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	return Response{ID: req.ID, Result: struct {
		Type string `json:"type"`
		limits.Snapshot
	}{"limits", s.limits.Snapshot(p.Refresh)}}
}
