package domain

import (
	"context"
	"time"
)

// SessionSnapshot captures the gist of a coding agent session at a point in time.
// It is used to resume context across agent restarts or session recreation,
// and as a searchable archive of completed work.
type SessionSnapshot struct {
	ID           string
	SessionID    string
	IssueKey     string
	Branch       string
	RepoName     string
	AgentName    string
	WorktreePath string
	// Summary is a one-line description for list subtitles (first overview sentence).
	Summary string
	// Overview contains the AI-generated overview sentences.
	Overview []string
	// Details contains the AI-generated detail bullet points.
	Details []string
	// Actions contains items that need immediate human attention.
	Actions []string
	// NextSteps are AI-extracted items that remain to be done.
	NextSteps []string
	// AgentState is the detected state at capture time.
	AgentState AgentState
	// PaneContent is the gzip-compressed, cleaned terminal tail used to generate
	// the summary. Use DecompressPaneContent to recover the plaintext.
	PaneContent []byte
	// InjectedAt records when this snapshot was last injected into a task file.
	// Nil means it has not been used for a resume yet.
	InjectedAt *time.Time
	CreatedAt  time.Time
}

// SessionSnapshotRepository defines persistence for session snapshots.
type SessionSnapshotRepository interface {
	// Save creates or updates a snapshot.
	Save(ctx context.Context, s *SessionSnapshot) error
	// GetLatest returns the most recent snapshot for a given session ID.
	// Returns nil, nil if none exists.
	GetLatest(ctx context.Context, sessionID string) (*SessionSnapshot, error)
	// List returns all snapshots for a session, newest first.
	List(ctx context.Context, sessionID string) ([]SessionSnapshot, error)
	// ListAll returns all snapshots across all sessions, newest first.
	ListAll(ctx context.Context) ([]SessionSnapshot, error)
	// MarkInjected records that a snapshot was used for a resume.
	MarkInjected(ctx context.Context, id string) error
	// Delete removes a snapshot by ID.
	Delete(ctx context.Context, id string) error
}
