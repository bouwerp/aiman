package usecase

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/bouwerp/aiman/internal/domain"
)

// SnapshotManager handles saving and loading session snapshots.
type SnapshotManager struct {
	db           domain.SessionRepository
	intelligence domain.IntelligenceProvider
}

func NewSnapshotManager(db domain.SessionRepository, intelligence domain.IntelligenceProvider) *SnapshotManager {
	return &SnapshotManager{db: db, intelligence: intelligence}
}

// SaveSnapshot captures the current state of a session as a snapshot.
// It uses the intelligence provider to generate a summary and extract next steps.
// paneContent is the cleaned terminal content to summarise.
func (m *SnapshotManager) SaveSnapshot(ctx context.Context, session *domain.Session, paneContent string) (*domain.SessionSnapshot, error) {
	summary, err := m.intelligence.SummariseSession(ctx, paneContent)
	if err != nil {
		// Fall back to a minimal snapshot without AI summary — still valuable for context.
		summary = &domain.SessionSummary{
			AgentState: domain.AgentStateUnknown,
			Summary:    "",
			Actions:    nil,
		}
	}

	nextSteps := make([]string, 0, len(summary.Actions))
	for _, a := range summary.Actions {
		nextSteps = append(nextSteps, a)
	}

	snap := &domain.SessionSnapshot{
		ID:          uuid.New().String(),
		SessionID:   session.ID,
		IssueKey:    session.IssueKey,
		Branch:      session.Branch,
		RepoName:    session.RepoName,
		AgentName:   session.AgentName,
		Summary:     summary.Summary,
		NextSteps:   nextSteps,
		AgentState:  summary.AgentState,
		PaneContent: paneContent,
		CreatedAt:   time.Now(),
	}

	if err := m.db.SaveSnapshot(ctx, snap); err != nil {
		return nil, fmt.Errorf("failed to persist snapshot: %w", err)
	}

	return snap, nil
}

// GetLatestSnapshot retrieves the most recent snapshot for a session.
func (m *SnapshotManager) GetLatestSnapshot(ctx context.Context, sessionID string) (*domain.SessionSnapshot, error) {
	return m.db.GetLatestSnapshot(ctx, sessionID)
}

// ListSnapshots returns all snapshots for a session.
func (m *SnapshotManager) ListSnapshots(ctx context.Context, sessionID string) ([]domain.SessionSnapshot, error) {
	return m.db.ListSnapshots(ctx, sessionID)
}

// ListAllSnapshots returns all snapshots across all sessions.
func (m *SnapshotManager) ListAllSnapshots(ctx context.Context) ([]domain.SessionSnapshot, error) {
	return m.db.ListAllSnapshots(ctx)
}
