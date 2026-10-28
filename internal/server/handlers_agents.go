package server

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/agentupdate"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/agent"
)

// agentsRefreshTimeout keeps an on-demand probe inside the 30s client
// deadline.
const agentsRefreshTimeout = 25 * time.Second

// SetAgentUpdates attaches the agent version manager and the host name
// reported by agents.status and agents.update.
func (s *Server) SetAgentUpdates(m *agentupdate.Manager, host string) {
	s.agentUpdates = m
	s.agentHost = host
}

// LiveAgentSessions lists the sessions the update gate checks: rows that are
// not stopped and whose agent has not ended, with the agent base command and
// the hook state when a fresh hook report has one.
func (s *Server) LiveAgentSessions(ctx context.Context) ([]agentupdate.LiveSession, error) {
	if s.repo == nil {
		return nil, nil
	}
	list, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var out []agentupdate.LiveSession
	for _, sess := range list {
		if !liveForUpdate(sess) {
			continue
		}
		base := agent.BaseOf(sess.AgentName)
		if base == "" {
			continue
		}
		state, _ := agenthook.ResolveHookState(sess, now)
		out = append(out, agentupdate.LiveSession{Agent: base, HookState: string(state)})
	}
	return out, nil
}

func liveForUpdate(sess domain.Session) bool {
	if sess.AgentEnded {
		return false
	}
	switch sess.Status {
	case domain.SessionStatusCleanup, domain.SessionStatusError, domain.SessionStatusInactive:
		return false
	default:
		return true
	}
}

type agentsStatusParams struct {
	Refresh bool `json:"refresh"`
}

func (s *Server) handleAgentsStatus(ctx context.Context, req Request) Response {
	if s.agentUpdates == nil {
		return errResp(req.ID, CodeInvalidParams, "agent updates are not running on this host")
	}
	var p agentsStatusParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	pctx, cancel := context.WithTimeout(ctx, agentsRefreshTimeout)
	defer cancel()
	list, err := s.agentUpdates.Status(pctx, p.Refresh)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type":   "agents",
		"host":   s.agentHost,
		"mode":   string(s.agentUpdates.Policy.Mode),
		"window": s.agentUpdates.Policy.Window.String(),
		"agents": list,
	}}
}

type agentsMovePrefixParams struct {
	Agent string `json:"agent"`
}

// handleAgentsMovePrefix reinstalls an npm agent under the user prefix in the
// background (Manager.MovePrefix). It never runs sudo and never uninstalls
// the root-owned copy.
func (s *Server) handleAgentsMovePrefix(ctx context.Context, req Request) Response {
	if s.agentUpdates == nil {
		return errResp(req.ID, CodeInvalidParams, "agent updates are not running on this host")
	}
	var p agentsMovePrefixParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	if strings.TrimSpace(p.Agent) == "" {
		return errResp(req.ID, CodeInvalidParams, "agent is required")
	}
	pctx, cancel := context.WithTimeout(ctx, agentsRefreshTimeout)
	defer cancel()
	out, err := s.agentUpdates.MovePrefix(pctx, p.Agent)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type":   "agents_move_prefix",
		"host":   s.agentHost,
		"result": out,
	}}
}

type agentsUpdateParams struct {
	Agent string `json:"agent"`
	All   bool   `json:"all"`
}

func (s *Server) handleAgentsUpdate(ctx context.Context, req Request) Response {
	if s.agentUpdates == nil {
		return errResp(req.ID, CodeInvalidParams, "agent updates are not running on this host")
	}
	var p agentsUpdateParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	p.Agent = strings.TrimSpace(p.Agent)
	if (p.Agent == "") == !p.All {
		return errResp(req.ID, CodeInvalidParams, "give agent or all, not both")
	}
	pctx, cancel := context.WithTimeout(ctx, agentsRefreshTimeout)
	defer cancel()
	out, err := s.agentUpdates.Request(pctx, p.Agent, p.All)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type":    "agents_update",
		"host":    s.agentHost,
		"results": out,
	}}
}
