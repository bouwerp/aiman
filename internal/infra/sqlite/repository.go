package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
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
		working_directory TEXT,
		tmux_session TEXT,
		mutagen_sync_id TEXT,
		local_path TEXT,
		agent_name TEXT,
		status TEXT,
		tunnels_json TEXT,
		created_at DATETIME,
		updated_at DATETIME
	);`

	if _, err := db.Exec(query); err != nil {
		return nil, fmt.Errorf("failed to create sessions table: %w", err)
	}

	// Add missing columns if they don't exist (for existing databases)
	_, _ = db.Exec("ALTER TABLE sessions ADD COLUMN mutagen_sync_id TEXT")
	_, _ = db.Exec("ALTER TABLE sessions ADD COLUMN local_path TEXT")
	_, _ = db.Exec("ALTER TABLE sessions ADD COLUMN working_directory TEXT")
	_, _ = db.Exec("ALTER TABLE sessions ADD COLUMN tunnels_json TEXT")

	return &Repository{
		db: db,
	}, nil
}

func (r *Repository) Save(ctx context.Context, s *domain.Session) error {
	var tunnelsJSON any
	if s.Tunnels != nil {
		encoded, err := json.Marshal(s.Tunnels)
		if err != nil {
			return fmt.Errorf("failed to encode session tunnels: %w", err)
		}
		tunnelsJSON = string(encoded)
	}

	query := `
	INSERT INTO sessions (id, issue_key, branch, repo_name, remote_host, worktree_path, working_directory, tmux_session, mutagen_sync_id, local_path, agent_name, status, tunnels_json, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		issue_key = excluded.issue_key,
		branch = excluded.branch,
		repo_name = excluded.repo_name,
		remote_host = excluded.remote_host,
		worktree_path = excluded.worktree_path,
		working_directory = excluded.working_directory,
		tmux_session = excluded.tmux_session,
		mutagen_sync_id = excluded.mutagen_sync_id,
		local_path = excluded.local_path,
		agent_name = excluded.agent_name,
		status = excluded.status,
		tunnels_json = COALESCE(excluded.tunnels_json, sessions.tunnels_json),
		updated_at = excluded.updated_at;
	`

	_, err := r.db.ExecContext(ctx, query,
		s.ID, s.IssueKey, s.Branch, s.RepoName, s.RemoteHost, s.WorktreePath, s.WorkingDirectory, s.TmuxSession, s.MutagenSyncID, s.LocalPath, s.AgentName, string(s.Status), tunnelsJSON, s.CreatedAt, time.Now())
	if err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}
	return nil
}

func (r *Repository) Get(ctx context.Context, id string) (*domain.Session, error) {
	query := "SELECT id, issue_key, branch, repo_name, remote_host, worktree_path, working_directory, tmux_session, mutagen_sync_id, local_path, agent_name, status, tunnels_json, created_at, updated_at FROM sessions WHERE id = ?;"

	var s domain.Session
	var statusStr string
	var issueKey, branch, repoName, remoteHost, worktreePath, workingDir, tmuxSession, mutagenSyncID, localPath, agentName, tunnelsJSON sql.NullString
	var createdAt, updatedAt sql.NullTime
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&s.ID, &issueKey, &branch, &repoName, &remoteHost, &worktreePath, &workingDir, &tmuxSession, &mutagenSyncID, &localPath, &agentName, &statusStr, &tunnelsJSON, &createdAt, &updatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("session not found: %w", err)
		}
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	s.IssueKey = issueKey.String
	s.Branch = branch.String
	s.RepoName = repoName.String
	s.RemoteHost = remoteHost.String
	s.WorktreePath = worktreePath.String
	s.WorkingDirectory = workingDir.String
	s.TmuxSession = tmuxSession.String
	s.MutagenSyncID = mutagenSyncID.String
	s.LocalPath = localPath.String
	s.AgentName = agentName.String
	s.Status = domain.SessionStatus(statusStr)
	if tunnelsJSON.Valid && tunnelsJSON.String != "" {
		if err := json.Unmarshal([]byte(tunnelsJSON.String), &s.Tunnels); err != nil {
			return nil, fmt.Errorf("failed to decode session tunnels: %w", err)
		}
	}
	s.CreatedAt = createdAt.Time
	s.UpdatedAt = updatedAt.Time
	return &s, nil
}

func (r *Repository) List(ctx context.Context) ([]domain.Session, error) {
	query := "SELECT id, issue_key, branch, repo_name, remote_host, worktree_path, working_directory, tmux_session, mutagen_sync_id, local_path, agent_name, status, tunnels_json, created_at, updated_at FROM sessions ORDER BY updated_at DESC;"

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []domain.Session
	for rows.Next() {
		var s domain.Session
		var statusStr string
		var issueKey, branch, repoName, remoteHost, worktreePath, workingDir, tmuxSession, mutagenSyncID, localPath, agentName, tunnelsJSON sql.NullString
		var createdAt, updatedAt sql.NullTime
		err := rows.Scan(&s.ID, &issueKey, &branch, &repoName, &remoteHost, &worktreePath, &workingDir, &tmuxSession, &mutagenSyncID, &localPath, &agentName, &statusStr, &tunnelsJSON, &createdAt, &updatedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan session: %w", err)
		}
		s.IssueKey = issueKey.String
		s.Branch = branch.String
		s.RepoName = repoName.String
		s.RemoteHost = remoteHost.String
		s.WorktreePath = worktreePath.String
		s.WorkingDirectory = workingDir.String
		s.TmuxSession = tmuxSession.String
		s.MutagenSyncID = mutagenSyncID.String
		s.LocalPath = localPath.String
		s.AgentName = agentName.String
		s.Status = domain.SessionStatus(statusStr)
		if tunnelsJSON.Valid && tunnelsJSON.String != "" {
			if err := json.Unmarshal([]byte(tunnelsJSON.String), &s.Tunnels); err != nil {
				return nil, fmt.Errorf("failed to decode session tunnels: %w", err)
			}
		}
		s.CreatedAt = createdAt.Time
		s.UpdatedAt = updatedAt.Time
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
