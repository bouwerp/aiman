package domain

import (
	"context"
	"errors"
)

// ErrIntelligenceUnavailable is returned when the local AI backend is not reachable
// or no model is available. Callers should degrade gracefully.
var ErrIntelligenceUnavailable = errors.New("local AI backend unavailable")

// IntelligenceProvider is the abstraction for all local LLM operations.
// Implementations: OllamaIntelligence, NoopIntelligence.
type IntelligenceProvider interface {
	// IsAvailable returns true if the backend is reachable and a suitable model is loaded.
	IsAvailable(ctx context.Context) bool

	// SummariseSession analyses tmux pane output and returns a structured summary
	// with detected agent state and action items.
	SummariseSession(ctx context.Context, paneContent string) (*SessionSummary, error)

	// DetectActions extracts actionable items from session output.
	DetectActions(ctx context.Context, paneContent string) ([]ActionItem, error)

	// SuggestPatterns recommends agentic orchestration patterns for a JIRA issue.
	SuggestPatterns(ctx context.Context, issue Issue) ([]PatternSuggestion, error)

	// GenerateCommitMessage drafts a conventional commit message from a git diff.
	GenerateCommitMessage(ctx context.Context, diff string) (string, error)
}

// SessionSummary is the structured result of analysing a coding agent's tmux output.
type SessionSummary struct {
	// Summary is a concise description of what the agent has been doing (≤60 words).
	Summary string `json:"summary"`
	// Actions are concrete things requiring human attention.
	Actions []string `json:"actions"`
	// AgentState describes the current agent activity level.
	AgentState AgentState `json:"agent_state"`
}

// AgentState describes the observable state of a coding agent in a tmux session.
type AgentState string

const (
	AgentStateIdle         AgentState = "idle"          // prompt shown, no activity
	AgentStateWorking      AgentState = "working"       // actively generating output
	AgentStateWaitingInput AgentState = "waiting_input" // asking a question or awaiting confirmation
	AgentStateErrored      AgentState = "errored"       // error visible in output
	AgentStateUnknown      AgentState = "unknown"
)

// ActionItem is a specific thing that requires human attention.
type ActionItem struct {
	// Type classifies the action needed.
	Type ActionItemType `json:"type"`
	// Message is a human-readable description.
	Message string `json:"message"`
	// Urgency indicates how time-sensitive the action is.
	Urgency ActionUrgency `json:"urgency"`
}

type ActionItemType string

const (
	ActionApprovalNeeded ActionItemType = "approval_needed"
	ActionErrorDetected  ActionItemType = "error_detected"
	ActionWaitingInput   ActionItemType = "waiting_input"
	ActionReviewReady    ActionItemType = "review_ready"
	ActionGeneral        ActionItemType = "general"
)

type ActionUrgency string

const (
	UrgencyHigh   ActionUrgency = "high"
	UrgencyMedium ActionUrgency = "medium"
	UrgencyLow    ActionUrgency = "low"
)

// PatternSuggestion recommends an agentic orchestration pattern for a task.
type PatternSuggestion struct {
	// Pattern is the name of the recommended pattern (e.g., "TDD Loop", "Explore-Plan-Implement").
	Pattern string `json:"pattern"`
	// Rationale explains why this pattern fits the issue.
	Rationale string `json:"rationale"`
	// PromptHints are concrete suggestions for how to prime the agent.
	PromptHints string `json:"prompt_hints"`
}
