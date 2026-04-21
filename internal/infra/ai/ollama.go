package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

const (
	defaultOllamaHost = "http://localhost:11434"
	defaultModel      = "qwen3:4b"
	fallbackModel     = "llama3.2:3b"
	maxPaneChars      = 9000 // tail of pane content sent to model (after cleaning)
	defaultNumCtx     = 16384 // KV cache size; safe on M-series with ≥16GB unified memory
	defaultMaxTokens  = 400
	inferenceTimeout  = 30 * time.Second
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
	sessionSummarySchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "summary":     {"type": "string"},
    "actions":     {"type": "array", "items": {"type": "string"}},
    "agent_state": {"type": "string", "enum": ["idle","working","waiting_input","errored","unknown"]}
  },
  "required": ["summary", "actions", "agent_state"]
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
	sessionSummarySystemPrompt = `You are a terse technical assistant monitoring AI coding agent terminal sessions.
Given tmux pane output, respond ONLY with valid JSON matching the schema.
agent_state: "idle" (shell prompt shown, waiting), "working" (producing output), "waiting_input" (asking user a question), "errored" (error visible), "unknown".
Keep summary under 60 words. Only list concrete action items in actions array.`

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
			Timeout: inferenceTimeout + 5*time.Second,
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

// SummariseSession analyses tmux pane output and returns a structured summary.
func (o *OllamaIntelligence) SummariseSession(ctx context.Context, paneContent string) (*domain.SessionSummary, error) {
	prompt := fmt.Sprintf("Analyse this terminal session output:\n\n```\n%s\n```", tailTruncate(cleanPaneContent(paneContent), maxPaneChars))

	raw, err := o.generate(ctx, sessionSummarySystemPrompt, prompt, sessionSummarySchema, defaultMaxTokens)
	if err != nil {
		return nil, err
	}

	var result struct {
		Summary    string            `json:"summary"`
		Actions    []string          `json:"actions"`
		AgentState domain.AgentState `json:"agent_state"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("failed to parse session summary: %w (raw: %.200s)", err, raw)
	}
	if result.AgentState == "" {
		result.AgentState = domain.AgentStateUnknown
	}
	return &domain.SessionSummary{
		Summary:    result.Summary,
		Actions:    result.Actions,
		AgentState: result.AgentState,
	}, nil
}

// DetectActions extracts actionable items from session output.
func (o *OllamaIntelligence) DetectActions(ctx context.Context, paneContent string) ([]domain.ActionItem, error) {
	prompt := fmt.Sprintf("Extract action items from this terminal output:\n\n```\n%s\n```", tailTruncate(cleanPaneContent(paneContent), maxPaneChars))

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
	reqCtx, cancel := context.WithTimeout(ctx, inferenceTimeout)
	defer cancel()

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

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, o.host+"/api/generate", bytes.NewReader(body))
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

// ansiEscape matches ANSI/VT100 escape sequences (colours, cursor moves, etc.).
var ansiEscape = regexp.MustCompile(`\x1b(\[[0-9;?]*[a-zA-Z]|\][^\x07]*\x07|[()][AB012]|[DABEGHM78=><]|%[Gg])`)

// cleanPaneContent strips token-wasting noise from tmux pane output before
// sending it to the model:
//  1. Remove ANSI/VT100 escape sequences (colour codes, cursor moves, etc.)
//  2. Collapse consecutive duplicate lines into "line [×N]"
//  3. Collapse runs of blank lines into a single blank line
//
// On typical terminal output this reduces character count by 40–60%.
func cleanPaneContent(s string) string {
	// 1. Strip ANSI escapes
	s = ansiEscape.ReplaceAllString(s, "")

	lines := strings.Split(s, "\n")

	// 2 & 3. Dedup + blank-collapse in one pass
	out := make([]string, 0, len(lines))
	prevLine := ""
	dupCount := 0
	blankRun := 0

	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t\r")

		if line == "" {
			blankRun++
			if blankRun == 1 {
				out = append(out, "")
			}
			// flush any pending dup before blank
			if dupCount > 1 {
				out[len(out)-2] = fmt.Sprintf("%s [×%d]", prevLine, dupCount)
			}
			prevLine = ""
			dupCount = 0
			continue
		}
		blankRun = 0

		if line == prevLine {
			dupCount++
			continue
		}

		// Flush previous dup group
		if dupCount > 1 && len(out) > 0 {
			out[len(out)-1] = fmt.Sprintf("%s [×%d]", prevLine, dupCount)
		}

		out = append(out, line)
		prevLine = line
		dupCount = 1
	}

	// Flush final dup group
	if dupCount > 1 && len(out) > 0 {
		out[len(out)-1] = fmt.Sprintf("%s [×%d]", prevLine, dupCount)
	}

	return strings.Join(out, "\n")
}
