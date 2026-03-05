package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/bouwerp/aiman/internal/domain"
)

type Repository struct {
	db *sql.DB
}

func NewRepository(dbPath string) (*Repository, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Create sessions table if not exists
	query := `
	CREATE TABLE IF NOT EXISTS sessions (
		id TEXT PRIMARY KEY,
		issue_key TEXT,
		branch TEXT,
		repo_name TEXT,
		remote_host TEXT,
		worktree_path TEXT,
		tmux_session TEXT,
		mutagen_sync_id TEXT,
		local_path TEXT,
		agent_name TEXT,
		status TEXT,
		created_at DATETIME,
		updated_at DATETIME
	);`

	if _, err := db.Exec(query); err != nil {
		return nil, fmt.Errorf("failed to create sessions table: %w", err)
	}

	// Add missing columns if they don't exist (for existing databases)
	db.Exec("ALTER TABLE sessions ADD COLUMN mutagen_sync_id TEXT")
	db.Exec("ALTER TABLE sessions ADD COLUMN local_path TEXT")

	return &Repository{
		db: db,
	}, nil
}

func (r *Repository) Save(ctx context.Context, s *domain.Session) error {
	query := `
	INSERT INTO sessions (id, issue_key, branch, repo_name, remote_host, worktree_path, tmux_session, mutagen_sync_id, local_path, agent_name, status, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		issue_key = excluded.issue_key,
		branch = excluded.branch,
		repo_name = excluded.repo_name,
		remote_host = excluded.remote_host,
		worktree_path = excluded.worktree_path,
		tmux_session = excluded.tmux_session,
		mutagen_sync_id = excluded.mutagen_sync_id,
		local_path = excluded.local_path,
		agent_name = excluded.agent_name,
		status = excluded.status,
		updated_at = excluded.updated_at;
	`

	_, err := r.db.ExecContext(ctx, query,
		s.ID, s.IssueKey, s.Branch, s.RepoName, s.RemoteHost, s.WorktreePath, s.TmuxSession, s.MutagenSyncID, s.LocalPath, s.AgentName, string(s.Status), s.CreatedAt, time.Now())
	if err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}
	return nil
}

func (r *Repository) Get(ctx context.Context, id string) (*domain.Session, error) {
	query := "SELECT id, issue_key, branch, repo_name, remote_host, worktree_path, tmux_session, mutagen_sync_id, local_path, agent_name, status, created_at, updated_at FROM sessions WHERE id = ?;"

	var s domain.Session
	var statusStr string
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&s.ID, &s.IssueKey, &s.Branch, &s.RepoName, &s.RemoteHost, &s.WorktreePath, &s.TmuxSession, &s.MutagenSyncID, &s.LocalPath, &s.AgentName, &statusStr, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("session not found: %w", err)
		}
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	s.Status = domain.SessionStatus(statusStr)
	return &s, nil
}

func (r *Repository) List(ctx context.Context) ([]domain.Session, error) {
	query := "SELECT id, issue_key, branch, repo_name, remote_host, worktree_path, tmux_session, mutagen_sync_id, local_path, agent_name, status, created_at, updated_at FROM sessions ORDER BY updated_at DESC;"

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []domain.Session
	for rows.Next() {
		var s domain.Session
		var statusStr string
		err := rows.Scan(&s.ID, &s.IssueKey, &s.Branch, &s.RepoName, &s.RemoteHost, &s.WorktreePath, &s.TmuxSession, &s.MutagenSyncID, &s.LocalPath, &s.AgentName, &statusStr, &s.CreatedAt, &s.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan session: %w", err)
		}
		s.Status = domain.SessionStatus(statusStr)
		sessions = append(sessions, s)
	}

	return sessions, nil
}

func (r *Repository) Delete(ctx context.Context, id string) error {
	query := "DELETE FROM sessions WHERE id = ?;"
	_, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}
	return nil
}

func (r *Repository) Close() error {
	return r.db.Close()
}
