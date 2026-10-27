package server

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/bouwerp/aiman/internal/memoryjob"
)

func (s *Server) handleMemoryJobs(ctx context.Context, req Request) Response {
	jobs := []memoryjob.Job{}
	if s.memoryJobs != nil {
		list, err := s.memoryJobs.Pending(ctx)
		if err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
		if list != nil {
			jobs = list
		}
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type": "memory_jobs",
		"jobs": jobs,
	}}
}

func (s *Server) handleMemoryDone(ctx context.Context, req Request) Response {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || strings.TrimSpace(p.ID) == "" {
		return errResp(req.ID, CodeInvalidParams, "id is required")
	}
	if s.memoryJobs == nil {
		return errResp(req.ID, CodeInvalidParams, "memory queue unavailable")
	}
	if err := s.memoryJobs.Done(ctx, p.ID); err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type": "memory_done",
		"id":   p.ID,
	}}
}
