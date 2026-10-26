package server

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/bouwerp/aiman/internal/usage"
)

// EnableUsage serves usage.get from rep and, when tr is set, scans transcripts
// every usage.ScanInterval until ctx is cancelled. A nil tracker is stored so
// later kicks stay no-ops.
func (s *Server) EnableUsage(ctx context.Context, tr *usage.Tracker, rep usage.Reporter) {
	cp := rep
	s.reporter = &cp
	s.usage = tr
	if tr == nil || ctx == nil {
		return
	}
	go tr.Run(ctx, usage.ScanInterval)
}

// PublishUsage tells event subscribers that one session's usage changed.
func (s *Server) PublishUsage(sessionID string) {
	if s == nil || s.hub == nil {
		return
	}
	s.hub.Publish(SessionEvent{Type: "usage_changed", ID: sessionID})
}

func (s *Server) kickUsage() {
	if s == nil || s.usage == nil {
		return
	}
	s.usage.Kick()
}

type usageGetParams struct {
	Session         string `json:"session"`
	Group           string `json:"group"`
	Issue           string `json:"issue"`
	Day             string `json:"day"`
	By              string `json:"by"`
	Days            int    `json:"days"`
	TZOffsetMinutes int    `json:"tz_offset_minutes"`
}

func (s *Server) handleUsageGet(ctx context.Context, req Request) Response {
	if s.reporter == nil {
		return errResp(req.ID, CodeInvalidParams, "usage tracking is not running on this host")
	}
	var p usageGetParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	if !validUsageBy(p.By) {
		return errResp(req.ID, CodeInvalidParams, "by must be session, issue, day, or model")
	}
	sessionID, fail, ok := s.resolveUsageSession(ctx, req.ID, p.Session)
	if !ok {
		return fail
	}
	rep, err := s.reporter.Report(ctx, usage.Query{
		SessionID:       sessionID,
		Group:           p.Group,
		Issue:           p.Issue,
		Day:             p.Day,
		By:              p.By,
		Days:            p.Days,
		TZOffsetMinutes: p.TZOffsetMinutes,
	})
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "usage", "report": rep}}
}

func validUsageBy(by string) bool {
	switch by {
	case "", "session", "issue", "day", "model":
		return true
	default:
		return false
	}
}

// resolveUsageSession matches todo.get: id, name, or group/name.
// An empty target is every session. An unknown target is not_found.
func (s *Server) resolveUsageSession(ctx context.Context, reqID, target string) (string, Response, bool) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", Response{}, true
	}
	list, err := s.loadSessions(ctx)
	if err != nil {
		return "", errResp(reqID, CodeInvalidParams, err.Error()), false
	}
	sess, ok := resolveSession(list, target)
	if !ok {
		return "", errResp(reqID, CodeNotFound, "session not found"), false
	}
	return sess.ID, Response{}, true
}
