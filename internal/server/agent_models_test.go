package server

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/usage"
)

type agentModelsResult struct {
	Type   string   `json:"type"`
	Agent  string   `json:"agent"`
	Models []string `json:"models"`
}

// TestAgentsModelsClaudeAddsSeenIDs answers Claude with its aliases, then the
// versioned ids the host's transcripts used, newest first.
func TestAgentsModelsClaudeAddsSeenIDs(t *testing.T) {
	srv := New(nil, testRepo(t), nil, nil, nil, nil, "test")
	now := time.Now().UTC().Truncate(time.Hour)
	srv.EnableUsage(context.Background(), nil, usage.Reporter{Store: usageFixture{buckets: []domain.UsageBucket{
		{SessionID: "a", Hour: now.Add(-time.Hour), Model: "claude-fable-5-1"},
		{SessionID: "a", Hour: now, Model: "claude-haiku-5-5"},
		{SessionID: "b", Hour: now, Model: "gpt-6.2-sol"},
	}}})
	var res agentModelsResult
	rpcOK(t, srv, "", "agents.models", map[string]any{"agent": "claude"}, &res)
	static := agent.LaunchCatalogFor("claude").Models
	if res.Type != "agent_models" || res.Agent != "claude" || !slices.Equal(res.Models[:len(static)], static) {
		t.Fatalf("result %+v", res)
	}
	if tail := res.Models[len(static):]; !slices.Equal(tail, []string{"claude-haiku-5-5", "claude-fable-5-1"}) {
		t.Fatalf("seen ids %v", tail)
	}
	wantHarnessCode(t, "no agent", rpc(t, srv, "", "agents.models", map[string]any{}), CodeInvalidParams)
}

// TestAgentsModelsListsInBackground answers from the catalog while the
// host's listing runs, then from the listing, and lists again after a
// refresh.
func TestAgentsModelsListsInBackground(t *testing.T) {
	srv := New(nil, testRepo(t), nil, nil, nil, nil, "test")
	var calls atomic.Int32
	release := make(chan struct{})
	srv.SetAgentModelLister(func(_ context.Context, command string) ([]agent.ModelOption, error) {
		n := calls.Add(1)
		if n == 1 {
			<-release
		}
		if command != "codex" {
			return nil, errors.New("unexpected " + command)
		}
		return []agent.ModelOption{{ID: "gpt-6.2-sol"}, {ID: "gpt-6-luna"}}, nil
	}, nil)
	var res agentModelsResult
	rpcOK(t, srv, "", "agents.models", map[string]any{"agent": "codex"}, &res)
	if !slices.Equal(res.Models, agent.LaunchCatalogFor("codex").Models) {
		t.Fatalf("first answer %v, want the catalog", res.Models)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		rpcOK(t, srv, "", "agents.models", map[string]any{"agent": "codex"}, &res)
		if slices.Equal(res.Models, []string{"gpt-6.2-sol", "gpt-6-luna"}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("listing never landed: %v", res.Models)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("listed %d times while fresh", n)
	}
	rpcOK(t, srv, "", "agents.models", map[string]any{"agent": "codex", "refresh": true}, &res)
	deadline = time.Now().Add(5 * time.Second)
	for calls.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("refresh did not list again")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
