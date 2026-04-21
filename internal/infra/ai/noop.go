package ai

import (
	"context"

	"github.com/bouwerp/aiman/internal/domain"
)

// NoopIntelligence is a no-op implementation of IntelligenceProvider.
// It is used when no AI backend is configured or available, allowing the app
// to start and function normally without AI features.
type NoopIntelligence struct{}

func (n *NoopIntelligence) IsAvailable(_ context.Context) bool { return false }

func (n *NoopIntelligence) SummariseBriefly(_ context.Context, _ string) (*domain.SessionSummary, error) {
	return nil, domain.ErrIntelligenceUnavailable
}

func (n *NoopIntelligence) SummariseSession(_ context.Context, _ string) (*domain.SessionSummary, error) {
	return nil, domain.ErrIntelligenceUnavailable
}

func (n *NoopIntelligence) DetectActions(_ context.Context, _ string) ([]domain.ActionItem, error) {
	return nil, domain.ErrIntelligenceUnavailable
}

func (n *NoopIntelligence) SuggestPatterns(_ context.Context, _ domain.Issue) ([]domain.PatternSuggestion, error) {
	return nil, domain.ErrIntelligenceUnavailable
}

func (n *NoopIntelligence) GenerateCommitMessage(_ context.Context, _ string) (string, error) {
	return "", domain.ErrIntelligenceUnavailable
}
