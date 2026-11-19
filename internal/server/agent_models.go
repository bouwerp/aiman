package server

import (
	"context"
	"encoding/json"
	"log"
	"os/exec"
	"slices"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/infra/agent"
)

// Agent model listings on this host. agents.status answers at once from what
// the last listing printed, else from LaunchCatalog, and starts a listing in
// the background when the stored one is missing or older than
// agentModelsTTL; an agent update or a status call with refresh drops them.
const (
	agentModelsTTL     = 6 * time.Hour
	agentModelsTimeout = 45 * time.Second
	// claudeSeenWindow is how far back the transcripts' Claude model ids count.
	claudeSeenWindow = 30 * 24 * time.Hour
)

// hostModels keeps one listing per agent base.
type hostModels struct {
	mu      sync.Mutex
	lists   map[string]listedModels
	running map[string]bool
	// list runs a listing; nil is the agent's own command on this host.
	list func(ctx context.Context, command string) ([]agent.ModelOption, error)
	now  func() time.Time
}

type listedModels struct {
	ids []string
	at  time.Time
}

// localShell runs a listing command on this host.
type localShell struct{}

func (localShell) Execute(ctx context.Context, cmd string) (string, error) {
	out, err := exec.CommandContext(ctx, "bash", "-c", cmd).Output() //nolint:gosec // G204: the command is one of agent.modelListers' fixed listings
	return string(out), err
}

func (h *hostModels) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// ids is the stored listing for base and whether it is fresh.
func (h *hostModels) ids(base string) ([]string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	l, ok := h.lists[base]
	return l.ids, ok && h.clock().Sub(l.at) < agentModelsTTL
}

// refresh lists base's models in the background unless a listing runs.
func (h *hostModels) refresh(base string) {
	h.mu.Lock()
	if h.running == nil {
		h.running = map[string]bool{}
	}
	if h.running[base] {
		h.mu.Unlock()
		return
	}
	h.running[base] = true
	list := h.list
	h.mu.Unlock()
	if list == nil {
		cache := agent.NewModelCache()
		list = func(ctx context.Context, command string) ([]agent.ModelOption, error) {
			return cache.List(ctx, "", localShell{}, command)
		}
	}
	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), agentModelsTimeout)
		defer cancel()
		opts, err := list(ctx, base)
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.running, base)
		if err != nil {
			log.Printf("agent models: %v", err)
			return
		}
		ids := make([]string, 0, len(opts))
		for _, o := range opts {
			ids = append(ids, o.ID)
		}
		if h.lists == nil {
			h.lists = map[string]listedModels{}
		}
		h.lists[base] = listedModels{ids: ids, at: h.clock()}
	}
	go run()
}

// Reset drops every listing, so the next status call lists again.
func (h *hostModels) Reset() {
	h.mu.Lock()
	h.lists = nil
	h.mu.Unlock()
}

// ResetAgentModels drops the stored listings; serve calls it after an agent
// update.
func (s *Server) ResetAgentModels() { s.agentModels.Reset() }

// SetAgentModelLister replaces the listing command (tests).
func (s *Server) SetAgentModelLister(list func(ctx context.Context, command string) ([]agent.ModelOption, error), now func() time.Time) {
	s.agentModels.mu.Lock()
	s.agentModels.list, s.agentModels.now = list, now
	s.agentModels.mu.Unlock()
}

type agentsModelsParams struct {
	Agent   string `json:"agent"`
	Refresh bool   `json:"refresh"`
}

// handleAgentsModels answers agents.models {agent, refresh?} with the model
// ids for that agent on this host (agentModelIDs). It reads only, needs no
// agent update manager, and backs `aiman agents models` and the TUI's
// Claude picker.
func (s *Server) handleAgentsModels(ctx context.Context, req Request) Response {
	var p agentsModelsParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	base := agent.BaseOf(p.Agent)
	if base == "" {
		return errResp(req.ID, CodeInvalidParams, "agent is required")
	}
	if p.Refresh {
		s.agentModels.Reset()
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type":   "agent_models",
		"agent":  base,
		"models": s.agentModelIDs(ctx, base),
	}}
}

// modelSeer is a usage reporter that lists the models transcripts used.
type modelSeer interface {
	SeenModels(ctx context.Context, since time.Time, prefix string) ([]string, error)
}

// agentModelIDs is the model list for an agent command on this host:
//   - an agent whose CLI lists its models: the stored listing, else
//     LaunchCatalog while a listing runs;
//   - Claude: its aliases, then the versioned ids its transcripts on this
//     host used in the last 30 days, newest first;
//   - any other agent: LaunchCatalog.
func (s *Server) agentModelIDs(ctx context.Context, command string) []string {
	base := agent.BaseOf(command)
	static := agent.LaunchCatalogFor(base).Models
	switch {
	case base == "claude":
		ids := slices.Clone(static)
		if seer, ok := s.reporter.(modelSeer); ok {
			seen, err := seer.SeenModels(ctx, time.Now().Add(-claudeSeenWindow), "claude-")
			if err != nil {
				log.Printf("agent models: claude: %v", err)
			}
			for _, id := range seen {
				if !slices.Contains(ids, id) {
					ids = append(ids, id)
				}
			}
		}
		return ids
	case agent.AgentLister(base):
		ids, fresh := s.agentModels.ids(base)
		if !fresh {
			s.agentModels.refresh(base)
		}
		if len(ids) > 0 {
			return ids
		}
	}
	return static
}
