package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/pane"
)

const (
	defaultOllamaHost = "http://localhost:11434"
	defaultModel      = "qwen3:4b"
	fallbackModel     = "llama3.2:3b"
	// MaxHeadChars and MaxTailChars are exported so the UI can show a preview
	// of exactly what gets sent to the model.
	MaxHeadChars      = 3000  // head of pane content — captures the initial user prompt
	MaxTailChars      = 12000 // tail of pane content — most recent activity
	defaultNumCtx     = 16384 // KV cache size; safe on M-series with ≥16GB unified memory
	defaultMaxTokens  = 1200
	httpClientTimeout = 120 * time.Second // ceiling for individual HTTP requests to Ollama
)

// ollamaGenerateRequest is the payload for POST /api/generate.
type ollamaGenerateRequest struct {
	Model   string          `json:"model"`
	System  string          `json:"system,omitempty"`
	Prompt  string          `json:"prompt"`
	Stream  bool            `json:"stream"`
	Think   bool            `json:"think"`
	Format  json.RawMessage `json:"format,omitempty"`
	Options map[string]any  `json:"options,omitempty"`
}

// ollamaGenerateResponse is the final (non-streaming) response from /api/generate.
type ollamaGenerateResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
	Error    string `json:"error,omitempty"`
}

// ollamaTagsResponse is returned by GET /api/tags.
type ollamaTagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

// JSON schemas for structured output.
var (
	sessionBriefSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "topic":      {"type": "string"},
    "summary":    {"type": "string"},
    "actions":    {"type": "array", "items": {"type": "string"}},
    "agent_state": {"type": "string", "enum": ["idle","working","waiting_input","errored","unknown"]}
  },
  "required": ["topic", "summary", "actions", "agent_state"]
}`)

	sessionSummarySchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "overview":   {"type": "array", "items": {"type": "string"}},
    "details":    {"type": "array", "items": {"type": "string"}},
    "actions":    {"type": "array", "items": {"type": "string"}},
    "next_steps": {"type": "array", "items": {"type": "string"}},
    "agent_state": {"type": "string", "enum": ["idle","working","waiting_input","errored","unknown"]}
  },
  "required": ["overview", "details", "actions", "next_steps", "agent_state"]
}`)

	actionItemsSchema = json.RawMessage(`{
  "type": "array",
  "items": {
    "type": "object",
    "properties": {
      "type":    {"type": "string", "enum": ["approval_needed","error_detected","waiting_input","review_ready","general"]},
      "message": {"type": "string"},
      "urgency": {"type": "string", "enum": ["high","medium","low"]}
    },
    "required": ["type", "message", "urgency"]
  }
}`)

	patternSuggestionsSchema = json.RawMessage(`{
  "type": "array",
  "items": {
    "type": "object",
    "properties": {
      "pattern":      {"type": "string"},
      "rationale":    {"type": "string"},
      "prompt_hints": {"type": "string"}
    },
    "required": ["pattern", "rationale", "prompt_hints"]
  }
}`)

	commitMessageSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "subject": {"type": "string"},
    "body":    {"type": "string"}
  },
  "required": ["subject"]
}`)
)

// System prompts for each use case.
const (
	sessionBriefSystemPrompt = `You are monitoring an AI coding agent's tmux session. Respond ONLY with valid JSON.
agent_state: idle|working|waiting_input|errored|unknown.
topic: ≤8 words describing what the session is about, derived from the initial user prompt at the start of SESSION START. Use noun phrases, no verbs. Examples: "AWS session-scoped credential isolation", "Auth module JWT refactor", "Fix pagination bug in orders API". If no clear initial prompt is visible, infer from the overall activity.
summary: one sentence describing current status. Write in present participle, NO subject. NEVER start with "The agent", "It", or any noun/pronoun subject. WRONG: "The agent is running tests." RIGHT: "Running tests after fixing the auth middleware timeout." Include file names or error text if visible.
actions: only items needing immediate human response (blocked on approval, unanswered question, unresolvable error). Empty array if none.`

	sessionSummarySystemPrompt = `You are monitoring an AI coding agent's tmux session. Respond ONLY with valid JSON.
When SESSION START is provided, treat the initial user prompt there as the primary goal of the session — use it to anchor the overview.
agent_state: idle|working|waiting_input|errored|unknown.
overview: array of 2-4 sentences, one per element. First sentence states the session goal (derived from the initial prompt). Remaining sentences cover accomplishments and current status. Write each in present participle — NO subject of any kind. NEVER use "The agent", "It", "The model", or any other subject. WRONG: "The agent implemented the archive flow." RIGHT: "Implemented the archive preview flow in dashboard.go."
details: array of 6-12 items — exact files created/modified/deleted, commands and outcomes, test pass/fail counts, errors verbatim, build and lint results. One sentence per item, no subject, present participle.
actions: items needing immediate human response (blocked on approval, unanswered question, unresolvable error). Empty array if none.
next_steps: concrete remaining tasks inferred from context. Empty array if none.`

	actionItemsSystemPrompt = `You are a technical assistant monitoring AI coding agent terminal sessions.
Extract any items requiring human attention from the terminal output.
Respond ONLY with a valid JSON array. Return an empty array [] if nothing needs attention.`

	patternSuggestionsSystemPrompt = `You are an expert in AI coding agents (Claude Code, Gemini CLI, Aider, Cursor, OpenCode).
Given a JIRA issue summary and description, suggest 2-3 agentic orchestration patterns.
Examples: "TDD Loop", "Explore-Plan-Implement", "Iterative Refinement", "Parallel Hypothesis Testing", "Spec-First".
Respond ONLY with a valid JSON array matching the schema.`

	commitMessageSystemPrompt = `Generate a conventional commit message from the git diff.
Format: type(scope): short description (max 72 chars).
Types: feat, fix, refactor, test, docs, chore, style, perf.
Optionally add a concise body for complex changes.
Respond ONLY with valid JSON matching the schema.`
)

// OllamaIntelligence implements IntelligenceProvider using the Ollama REST API.
// It uses a plain net/http client — no external dependencies required.
type OllamaIntelligence struct {
	host   string
	model  string
	client *http.Client
}

// NewOllamaIntelligence creates a new Ollama-backed intelligence provider.
// host defaults to http://localhost:11434; model defaults to qwen3:4b.
func NewOllamaIntelligence(host, model string) *OllamaIntelligence {
	if host == "" {
		host = defaultOllamaHost
	}
	if model == "" {
		model = defaultModel
	}
	return &OllamaIntelligence{
		host:  strings.TrimRight(host, "/"),
		model: model,
		client: &http.Client{
			Timeout: httpClientTimeout,
		},
	}
}

// IsAvailable checks that Ollama is running and the configured model is present.
func (o *OllamaIntelligence) IsAvailable(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.host+"/api/tags", nil)
	if err != nil {
		return false
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var tags ollamaTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return false
	}
	for _, m := range tags.Models {
		if strings.HasPrefix(m.Name, o.model) {
			return true
		}
	}
	// Also accept if any fallback model is present
	for _, m := range tags.Models {
		if strings.HasPrefix(m.Name, fallbackModel) {
			o.model = fallbackModel
			return true
		}
	}
	return false
}

// SummariseBriefly produces a compact status summary for the session browser sidebar.
func (o *OllamaIntelligence) SummariseBriefly(ctx context.Context, paneContent string) (*domain.SessionSummary, error) {
	cleaned := pane.Clean(paneContent)
	head := headTruncate(cleaned, MaxHeadChars)
	tail := tailTruncate(cleaned, MaxTailChars)

	var prompt string
	if head != tail {
		prompt = fmt.Sprintf(
			"SESSION START (initial task / first prompt):\n```\n%s\n```\n\nSESSION RECENT ACTIVITY:\n```\n%s\n```",
			head, tail,
		)
	} else {
		prompt = fmt.Sprintf("Terminal output:\n\n```\n%s\n```", tail)
	}

	raw, err := o.generate(ctx, sessionBriefSystemPrompt, prompt, sessionBriefSchema, 250)
	if err != nil {
		return nil, err
	}

	var result struct {
		Topic      string            `json:"topic"`
		Summary    string            `json:"summary"`
		Actions    []string          `json:"actions"`
		AgentState domain.AgentState `json:"agent_state"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("failed to parse brief summary: %w (raw: %.200s)", err, raw)
	}
	if result.AgentState == "" {
		result.AgentState = domain.AgentStateUnknown
	}
	return &domain.SessionSummary{
		Topic:      result.Topic,
		Summary:    result.Summary,
		Actions:    result.Actions,
		AgentState: result.AgentState,
	}, nil
}

// SummariseSession produces a full structured summary for archiving.
func (o *OllamaIntelligence) SummariseSession(ctx context.Context, paneContent string) (*domain.SessionSummary, error) {
	head := headTruncate(paneContent, MaxHeadChars)
	tail := tailTruncate(paneContent, MaxTailChars)

	var prompt string
	if head != tail {
		// Long session: show opening (initial task) + recent work separately.
		prompt = fmt.Sprintf(
			"SESSION START (initial task / first prompt):\n```\n%s\n```\n\nSESSION RECENT ACTIVITY:\n```\n%s\n```",
			head, tail,
		)
	} else {
		prompt = fmt.Sprintf("Analyse this terminal session output:\n\n```\n%s\n```", tail)
	}

	raw, err := o.generate(ctx, sessionSummarySystemPrompt, prompt, sessionSummarySchema, defaultMaxTokens)
	if err != nil {
		return nil, err
	}

	var result struct {
		Overview   []string          `json:"overview"`
		Details    []string          `json:"details"`
		Actions    []string          `json:"actions"`
		NextSteps  []string          `json:"next_steps"`
		AgentState domain.AgentState `json:"agent_state"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("failed to parse session summary: %w (raw: %.200s)", err, raw)
	}
	if result.AgentState == "" {
		result.AgentState = domain.AgentStateUnknown
	}
	return &domain.SessionSummary{
		Overview:   result.Overview,
		Details:    result.Details,
		Actions:    result.Actions,
		NextSteps:  result.NextSteps,
		AgentState: result.AgentState,
	}, nil
}

// DetectActions extracts actionable items from session output.
func (o *OllamaIntelligence) DetectActions(ctx context.Context, paneContent string) ([]domain.ActionItem, error) {
	prompt := fmt.Sprintf("Extract action items from this terminal output:\n\n```\n%s\n```", tailTruncate(pane.Clean(paneContent), MaxTailChars))

	raw, err := o.generate(ctx, actionItemsSystemPrompt, prompt, actionItemsSchema, 300)
	if err != nil {
		return nil, err
	}

	// The response may be wrapped in an object; try array first
	raw = strings.TrimSpace(raw)
	var items []struct {
		Type    domain.ActionItemType `json:"type"`
		Message string                `json:"message"`
		Urgency domain.ActionUrgency  `json:"urgency"`
	}
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, fmt.Errorf("failed to parse action items: %w (raw: %.200s)", err, raw)
	}
	result := make([]domain.ActionItem, 0, len(items))
	for _, it := range items {
		if it.Type == "" {
			it.Type = domain.ActionGeneral
		}
		if it.Urgency == "" {
			it.Urgency = domain.UrgencyMedium
		}
		result = append(result, domain.ActionItem{
			Type:    it.Type,
			Message: it.Message,
			Urgency: it.Urgency,
		})
	}
	return result, nil
}

// SuggestPatterns recommends agentic orchestration patterns for a JIRA issue.
func (o *OllamaIntelligence) SuggestPatterns(ctx context.Context, issue domain.Issue) ([]domain.PatternSuggestion, error) {
	prompt := fmt.Sprintf("JIRA Issue: %s\nSummary: %s\nDescription: %s",
		issue.Key,
		issue.Summary,
		tailTruncate(issue.Description, 1000),
	)

	raw, err := o.generate(ctx, patternSuggestionsSystemPrompt, prompt, patternSuggestionsSchema, 500)
	if err != nil {
		return nil, err
	}

	var items []struct {
		Pattern     string `json:"pattern"`
		Rationale   string `json:"rationale"`
		PromptHints string `json:"prompt_hints"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &items); err != nil {
		return nil, fmt.Errorf("failed to parse pattern suggestions: %w (raw: %.200s)", err, raw)
	}
	result := make([]domain.PatternSuggestion, 0, len(items))
	for _, it := range items {
		result = append(result, domain.PatternSuggestion{
			Pattern:     it.Pattern,
			Rationale:   it.Rationale,
			PromptHints: it.PromptHints,
		})
	}
	return result, nil
}

// GenerateCommitMessage drafts a conventional commit message from a git diff.
func (o *OllamaIntelligence) GenerateCommitMessage(ctx context.Context, diff string) (string, error) {
	prompt := fmt.Sprintf("Git diff:\n\n```diff\n%s\n```", tailTruncate(diff, 3000))

	raw, err := o.generate(ctx, commitMessageSystemPrompt, prompt, commitMessageSchema, 200)
	if err != nil {
		return "", err
	}

	var result struct {
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &result); err != nil {
		return "", fmt.Errorf("failed to parse commit message: %w (raw: %.200s)", err, raw)
	}
	if result.Body != "" {
		return result.Subject + "\n\n" + result.Body, nil
	}
	return result.Subject, nil
}

// generate sends a single-turn generation request to Ollama.
func (o *OllamaIntelligence) generate(ctx context.Context, system, prompt string, schema json.RawMessage, maxTokens int) (string, error) {
	payload := ollamaGenerateRequest{
		Model:  o.model,
		System: system,
		Prompt: prompt,
		Stream: false,
		Think:  false, // disable thinking tokens (critical for qwen3 speed)
		Format: schema,
		Options: map[string]any{
			"temperature": 0.1,
			"num_predict": maxTokens,
			"num_ctx":     defaultNumCtx, // critical: caps KV cache, prevents VRAM exhaustion
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal ollama request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.host+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("failed to create ollama request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read ollama response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama returned status %d: %.300s", resp.StatusCode, string(respBody))
	}

	var genResp ollamaGenerateResponse
	if err := json.Unmarshal(respBody, &genResp); err != nil {
		return "", fmt.Errorf("failed to decode ollama response: %w", err)
	}
	if genResp.Error != "" {
		return "", fmt.Errorf("ollama error: %s", genResp.Error)
	}

	response := strings.TrimSpace(genResp.Response)
	if response == "" {
		return "", fmt.Errorf("ollama returned an empty response")
	}
	return response, nil
}

// tailTruncate returns the last maxChars characters of s, prefixed with a truncation notice.
// Keeping the tail is important for tmux pane content — the most recent output is most relevant.
func tailTruncate(s string, maxChars int) string {
	if len(s) <= maxChars {
		return s
	}
	return "...[truncated]\n" + s[len(s)-maxChars:]
}

// headTruncate returns the first maxChars characters of s.
func headTruncate(s string, maxChars int) string {
	if len(s) <= maxChars {
		return s
	}
	return s[:maxChars] + "\n...[truncated]"
}
