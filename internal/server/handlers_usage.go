package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/usage"
)

// usageReporter prices a query. *usage.Reporter satisfies it.
type usageReporter interface {
	Report(ctx context.Context, q usage.Query) (usage.Report, error)
}

type usageHealthReporter interface {
	HealthFor(ctx context.Context, q usage.Query) (usage.Health, error)
}

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
	s.hub.Publish(SessionEvent{Type: "usage", ID: sessionID})
}

// SetUsageDefaultBudget records usage.budgets.default_tokens.
func (s *Server) SetUsageDefaultBudget(tokens int64) {
	if s == nil {
		return
	}
	s.usageDefaultBudget = tokens
}

// PublishBudget records a budget threshold in the ledger and on the hub.
// It never stops the agent.
func (s *Server) PublishBudget(sessionID, state string) {
	if s == nil {
		return
	}
	s.appendEvent(context.Background(), sessionID, domain.EventActorAiman, domain.EventUsageBudget, map[string]any{"state": state})
	if s.hub != nil {
		s.hub.Publish(SessionEvent{Type: "usage.budget", ID: sessionID, State: state})
	}
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
		return errResp(req.ID, CodeInvalidParams, "by must be session, issue, day, model, or role")
	}
	sessionID, fail, ok := s.resolveUsageSession(ctx, req.ID, p.Session)
	if !ok {
		return fail
	}
	query := usage.Query{
		SessionID:       sessionID,
		Group:           p.Group,
		Issue:           p.Issue,
		Day:             p.Day,
		By:              p.By,
		Days:            p.Days,
		TZOffsetMinutes: p.TZOffsetMinutes,
	}
	rep, err := s.reporter.Report(ctx, query)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	health := usage.Health{}
	if hr, ok := s.reporter.(usageHealthReporter); ok {
		health, err = hr.HealthFor(ctx, query)
		if err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	budget := s.usageBudget(ctx, sessionID, rep)
	return Response{ID: req.ID, Result: map[string]any{
		"type":    "usage",
		"session": sessionID,
		"usage":   rep,
		"health":  health,
		"budget":  budget,
	}}
}

// usageBudget prices one session's report against its budget.
// A host-wide query has no single budget and returns zero.
func (s *Server) usageBudget(ctx context.Context, sessionID string, rep usage.Report) usage.Budget {
	tokens := rep.Total.Tokens
	used := tokens.Input + tokens.Output + tokens.CacheRead + tokens.CacheCreation()
	if sessionID == "" {
		return usage.Budget{Used: used}
	}
	budget := s.usageDefaultBudget
	if list, err := s.loadSessions(ctx); err == nil {
		if sess, ok := resolveSession(list, sessionID); ok && sess.BudgetTokens > 0 {
			budget = sess.BudgetTokens
		}
	}
	return usage.BudgetFor(budget, used)
}

func validUsageBy(by string) bool {
	switch by {
	case "", "session", "issue", "day", "model", "role":
		return true
	default:
		return false
	}
}

// resolveUsageSession matches id, name, or group/name.
// An empty target is every session. An unknown target is invalid_params.
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
		return "", errResp(reqID, CodeInvalidParams, fmt.Sprintf("unknown session %q", target)), false
	}
	return sess.ID, Response{}, true
}
