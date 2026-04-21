package ai_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/ai"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// TestNoopIntelligence verifies that NoopIntelligence always reports unavailable
// and returns ErrIntelligenceUnavailable for every method.
func TestNoopIntelligence(t *testing.T) {
	noop := ai.NewIntelligenceProvider(&config.Config{AI: config.AIConfig{Enabled: false}})
	ctx := context.Background()

	if noop.IsAvailable(ctx) {
		t.Error("NoopIntelligence.IsAvailable should return false")
	}

	if _, err := noop.SummariseSession(ctx, "some output"); err != domain.ErrIntelligenceUnavailable {
		t.Errorf("SummariseSession: want ErrIntelligenceUnavailable, got %v", err)
	}

	if _, err := noop.DetectActions(ctx, "some output"); err != domain.ErrIntelligenceUnavailable {
		t.Errorf("DetectActions: want ErrIntelligenceUnavailable, got %v", err)
	}

	if _, err := noop.SuggestPatterns(ctx, domain.Issue{}); err != domain.ErrIntelligenceUnavailable {
		t.Errorf("SuggestPatterns: want ErrIntelligenceUnavailable, got %v", err)
	}

	if _, err := noop.GenerateCommitMessage(ctx, "diff"); err != domain.ErrIntelligenceUnavailable {
		t.Errorf("GenerateCommitMessage: want ErrIntelligenceUnavailable, got %v", err)
	}
}

// TestFactoryReturnsNoopWhenDisabled verifies the factory returns noop if AI.Enabled is false.
func TestFactoryReturnsNoopWhenDisabled(t *testing.T) {
	cfg := &config.Config{}
	cfg.AI.Enabled = false
	p := ai.NewIntelligenceProvider(cfg)
	if p.IsAvailable(context.Background()) {
		t.Error("expected unavailable when AI disabled")
	}
}

// TestFactoryReturnsNoopForNilConfig verifies factory handles nil config safely.
func TestFactoryReturnsNoopForNilConfig(t *testing.T) {
	p := ai.NewIntelligenceProvider(nil)
	if p.IsAvailable(context.Background()) {
		t.Error("expected unavailable for nil config")
	}
}

// TestOllamaIntelligenceSummariseSession verifies the Ollama client parses a
// well-formed JSON response from the /api/generate endpoint.
func TestOllamaIntelligenceSummariseSession(t *testing.T) {
	summary := domain.SessionSummary{
		Summary:    "Agent is writing tests for the auth module.",
		Actions:    []string{"Review generated tests"},
		AgentState: domain.AgentStateWorking,
	}
	rawJSON, _ := json.Marshal(summary)

	// Fake Ollama server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			http.NotFound(w, r)
			return
		}
		resp := map[string]interface{}{
			"response": string(rawJSON),
			"done":     true,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := ai.NewOllamaIntelligence(srv.URL, "test-model")
	result, err := client.SummariseSession(context.Background(), "some pane content")
	if err != nil {
		t.Fatalf("SummariseSession error: %v", err)
	}
	if result.Summary != summary.Summary {
		t.Errorf("Summary: want %q, got %q", summary.Summary, result.Summary)
	}
	if result.AgentState != domain.AgentStateWorking {
		t.Errorf("AgentState: want %q, got %q", domain.AgentStateWorking, result.AgentState)
	}
	if len(result.Actions) != 1 || result.Actions[0] != "Review generated tests" {
		t.Errorf("Actions: unexpected %v", result.Actions)
	}
}

// TestOllamaIntelligenceIsAvailableTrue verifies IsAvailable returns true when Ollama responds.
func TestOllamaIntelligenceIsAvailableTrue(t *testing.T) {
	tagsResp := map[string]interface{}{
		"models": []map[string]interface{}{
			{"name": "test-model"},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(tagsResp)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client := ai.NewOllamaIntelligence(srv.URL, "test-model")
	if !client.IsAvailable(context.Background()) {
		t.Error("expected IsAvailable true when server responds")
	}
}

// TestOllamaIntelligenceIsAvailableFalse verifies IsAvailable returns false when Ollama is down.
func TestOllamaIntelligenceIsAvailableFalse(t *testing.T) {
	// Closed server — connection refused
	client := ai.NewOllamaIntelligence("http://127.0.0.1:19999", "test-model")
	if client.IsAvailable(context.Background()) {
		t.Error("expected IsAvailable false when server is unreachable")
	}
}
