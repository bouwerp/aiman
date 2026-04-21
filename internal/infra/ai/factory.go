package ai

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// NewIntelligenceProvider constructs the appropriate IntelligenceProvider from config.
// It never blocks: availability is checked lazily on first use via LazyIntelligence.
// If AI is disabled or misconfigured, a NoopIntelligence is returned.
func NewIntelligenceProvider(cfg *config.Config) domain.IntelligenceProvider {
	if cfg == nil || !cfg.AI.Enabled {
		return &NoopIntelligence{}
	}
	host := cfg.AI.OllamaHost
	if host == "" {
		host = defaultOllamaHost
	}
	model := cfg.AI.Model
	if model == "" {
		model = defaultModel
	}
	return newLazyIntelligence(NewOllamaIntelligence(host, model))
}

// lazyIntelligence wraps a delegate IntelligenceProvider and defers the availability
// check to the first call. This prevents blocking app startup on an Ollama health check.
type lazyIntelligence struct {
	delegate  *OllamaIntelligence
	once      sync.Once
	available atomic.Bool
}

func newLazyIntelligence(delegate *OllamaIntelligence) *lazyIntelligence {
	return &lazyIntelligence{delegate: delegate}
}

func (l *lazyIntelligence) checkOnce(ctx context.Context) bool {
	l.once.Do(func() {
		l.available.Store(l.delegate.IsAvailable(ctx))
	})
	return l.available.Load()
}

func (l *lazyIntelligence) IsAvailable(ctx context.Context) bool {
	return l.checkOnce(ctx)
}

func (l *lazyIntelligence) SummariseBriefly(ctx context.Context, paneContent string) (*domain.SessionSummary, error) {
	if !l.checkOnce(ctx) {
		return nil, domain.ErrIntelligenceUnavailable
	}
	return l.delegate.SummariseBriefly(ctx, paneContent)
}

func (l *lazyIntelligence) SummariseSession(ctx context.Context, paneContent string) (*domain.SessionSummary, error) {
	if !l.checkOnce(ctx) {
		return nil, domain.ErrIntelligenceUnavailable
	}
	return l.delegate.SummariseSession(ctx, paneContent)
}

func (l *lazyIntelligence) DetectActions(ctx context.Context, paneContent string) ([]domain.ActionItem, error) {
	if !l.checkOnce(ctx) {
		return nil, domain.ErrIntelligenceUnavailable
	}
	return l.delegate.DetectActions(ctx, paneContent)
}

func (l *lazyIntelligence) SuggestPatterns(ctx context.Context, issue domain.Issue) ([]domain.PatternSuggestion, error) {
	if !l.checkOnce(ctx) {
		return nil, domain.ErrIntelligenceUnavailable
	}
	return l.delegate.SuggestPatterns(ctx, issue)
}

func (l *lazyIntelligence) GenerateCommitMessage(ctx context.Context, diff string) (string, error) {
	if !l.checkOnce(ctx) {
		return "", domain.ErrIntelligenceUnavailable
	}
	return l.delegate.GenerateCommitMessage(ctx, diff)
}
