package usecase

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/pane"
)

// SnapshotManager handles saving and loading session snapshots.
type SnapshotManager struct {
	db           domain.SessionRepository
	intelligence domain.IntelligenceProvider
}

func NewSnapshotManager(db domain.SessionRepository, intelligence domain.IntelligenceProvider) *SnapshotManager {
	return &SnapshotManager{db: db, intelligence: intelligence}
}

// compressPaneContent gzip-compresses plaintext pane content for storage.
func compressPaneContent(s string) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte(s)); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DecompressPaneContent decompresses gzip-compressed pane content back to plaintext.
// Returns an empty string if b is nil or empty.
func DecompressPaneContent(b []byte) (string, error) {
	if len(b) == 0 {
		return "", nil
	}
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	defer r.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// SaveSnapshot captures the current state of a session as a snapshot.
// The raw pane content is cleaned, structurally compressed, and gzip-compressed
// before being persisted. The same cleaned text is passed to the AI for summarisation.
func (m *SnapshotManager) SaveSnapshot(ctx context.Context, session *domain.Session, rawPane string) (*domain.SessionSnapshot, error) {
	cleaned := pane.Clean(rawPane)

	summary, err := m.intelligence.SummariseSession(ctx, cleaned)
	if err != nil {
		summary = &domain.SessionSummary{AgentState: domain.AgentStateUnknown}
	}

	compressed, err := compressPaneContent(cleaned)
	if err != nil {
		compressed = nil
	}

	snap := &domain.SessionSnapshot{
		ID:           uuid.New().String(),
		SessionID:    session.ID,
		IssueKey:     session.IssueKey,
		Branch:       session.Branch,
		RepoName:     session.RepoName,
		AgentName:    session.AgentName,
		WorktreePath: session.WorktreePath,
		Summary:      strings.Join(summary.Overview, " "),
		NextSteps:    summary.NextSteps,
		AgentState:   summary.AgentState,
		PaneContent:  compressed,
		CreatedAt:    time.Now(),
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

// PreviewSnapshot runs the full clean+compress+AI pipeline but does NOT persist
// the result. Returns the draft snapshot, the full AI summary (for display), and
// the compressed byte size.
func (m *SnapshotManager) PreviewSnapshot(ctx context.Context, session *domain.Session, rawPane string) (*domain.SessionSnapshot, *domain.SessionSummary, int, error) {
	cleaned := pane.Clean(rawPane)

	summary, err := m.intelligence.SummariseSession(ctx, cleaned)
	if err != nil {
		summary = &domain.SessionSummary{AgentState: domain.AgentStateUnknown}
	}

	compressed, err := compressPaneContent(cleaned)
	if err != nil {
		compressed = nil
	}

	snap := &domain.SessionSnapshot{
		ID:           uuid.New().String(),
		SessionID:    session.ID,
		IssueKey:     session.IssueKey,
		Branch:       session.Branch,
		RepoName:     session.RepoName,
		AgentName:    session.AgentName,
		WorktreePath: session.WorktreePath,
		Summary:      strings.Join(summary.Overview, " "),
		NextSteps:    summary.NextSteps,
		AgentState:   summary.AgentState,
		PaneContent:  compressed,
		CreatedAt:    time.Now(),
	}

	return snap, summary, len(compressed), nil
}

// PersistSnapshot saves a pre-built snapshot (e.g. from PreviewSnapshot) to the database.
func (m *SnapshotManager) PersistSnapshot(ctx context.Context, snap *domain.SessionSnapshot) error {
	if err := m.db.SaveSnapshot(ctx, snap); err != nil {
		return fmt.Errorf("failed to persist snapshot: %w", err)
	}
	return nil
}

// ListSnapshots returns all snapshots for a session.
func (m *SnapshotManager) ListSnapshots(ctx context.Context, sessionID string) ([]domain.SessionSnapshot, error) {
	return m.db.ListSnapshots(ctx, sessionID)
}

// ListAllSnapshots returns all snapshots across all sessions.
func (m *SnapshotManager) ListAllSnapshots(ctx context.Context) ([]domain.SessionSnapshot, error) {
	return m.db.ListAllSnapshots(ctx)
}

// DeleteSnapshot removes a snapshot by ID.
func (m *SnapshotManager) DeleteSnapshot(ctx context.Context, id string) error {
	return m.db.DeleteSnapshot(ctx, id)
}
