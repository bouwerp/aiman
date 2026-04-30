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
	_, _ = db.Exec("ALTER TABLE sessions ADD COLUMN aws_profile TEXT")
	_, _ = db.Exec("ALTER TABLE sessions ADD COLUMN aws_config_json TEXT")

	secretsQuery := `
	CREATE TABLE IF NOT EXISTS secrets (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT ''
	);`
	if _, err := db.Exec(secretsQuery); err != nil {
		return nil, fmt.Errorf("failed to create secrets table: %w", err)
	}

	snapQuery := `
	CREATE TABLE IF NOT EXISTS session_snapshots (
		id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL,
		issue_key TEXT,
		branch TEXT,
		repo_name TEXT,
		agent_name TEXT,
		worktree_path TEXT,
		summary TEXT,
		next_steps_json TEXT,
		agent_state TEXT,
		pane_content BLOB,
		injected_at DATETIME,
		created_at DATETIME NOT NULL
	);`
	if _, err := db.Exec(snapQuery); err != nil {
		return nil, fmt.Errorf("failed to create session_snapshots table: %w", err)
	}
	_, _ = db.Exec("ALTER TABLE session_snapshots ADD COLUMN worktree_path TEXT")
	_, _ = db.Exec("ALTER TABLE session_snapshots ADD COLUMN overview_json TEXT")
	_, _ = db.Exec("ALTER TABLE session_snapshots ADD COLUMN details_json TEXT")
	_, _ = db.Exec("ALTER TABLE session_snapshots ADD COLUMN actions_json TEXT")

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

	var awsConfigJSON string
	if s.AWSConfig != nil {
		if b, err := json.Marshal(s.AWSConfig); err == nil {
			awsConfigJSON = string(b)
		}
	}

	query := `
	INSERT INTO sessions (id, issue_key, branch, repo_name, remote_host, worktree_path, working_directory, tmux_session, mutagen_sync_id, local_path, agent_name, status, tunnels_json, aws_profile, aws_config_json, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		issue_key = COALESCE(NULLIF(excluded.issue_key, ''), sessions.issue_key),
		branch = COALESCE(NULLIF(excluded.branch, ''), sessions.branch),
		repo_name = COALESCE(NULLIF(excluded.repo_name, ''), sessions.repo_name),
		remote_host = COALESCE(NULLIF(excluded.remote_host, ''), sessions.remote_host),
		worktree_path = COALESCE(NULLIF(excluded.worktree_path, ''), sessions.worktree_path),
		working_directory = COALESCE(NULLIF(excluded.working_directory, ''), sessions.working_directory),
		tmux_session = COALESCE(NULLIF(excluded.tmux_session, ''), sessions.tmux_session),
		mutagen_sync_id = COALESCE(NULLIF(excluded.mutagen_sync_id, ''), sessions.mutagen_sync_id),
		local_path = COALESCE(NULLIF(excluded.local_path, ''), sessions.local_path),
		agent_name = COALESCE(NULLIF(excluded.agent_name, ''), sessions.agent_name),
		status = excluded.status,
		tunnels_json = COALESCE(excluded.tunnels_json, sessions.tunnels_json),
		aws_profile = COALESCE(excluded.aws_profile, sessions.aws_profile),
		aws_config_json = COALESCE(excluded.aws_config_json, sessions.aws_config_json),
		updated_at = excluded.updated_at;
	`

	_, err := r.db.ExecContext(ctx, query,
		s.ID, s.IssueKey, s.Branch, s.RepoName, s.RemoteHost, s.WorktreePath, s.WorkingDirectory, s.TmuxSession, s.MutagenSyncID, s.LocalPath, s.AgentName, string(s.Status), tunnelsJSON, s.AWSProfileName, awsConfigJSON, s.CreatedAt, time.Now())
	if err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}
	return nil
}

func (r *Repository) Get(ctx context.Context, id string) (*domain.Session, error) {
	query := "SELECT id, issue_key, branch, repo_name, remote_host, worktree_path, working_directory, tmux_session, mutagen_sync_id, local_path, agent_name, status, tunnels_json, aws_profile, aws_config_json, created_at, updated_at FROM sessions WHERE id = ?;"

	var s domain.Session
	var statusStr string
	var issueKey, branch, repoName, remoteHost, worktreePath, workingDir, tmuxSession, mutagenSyncID, localPath, agentName, tunnelsJSON, awsProfile, awsConfigJSON sql.NullString
	var createdAt, updatedAt sql.NullTime
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&s.ID, &issueKey, &branch, &repoName, &remoteHost, &worktreePath, &workingDir, &tmuxSession, &mutagenSyncID, &localPath, &agentName, &statusStr, &tunnelsJSON, &awsProfile, &awsConfigJSON, &createdAt, &updatedAt)
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
	s.AWSProfileName = awsProfile.String
	if awsConfigJSON.Valid && awsConfigJSON.String != "" {
		var cfg domain.AWSConfig
		if err := json.Unmarshal([]byte(awsConfigJSON.String), &cfg); err == nil {
			s.AWSConfig = &cfg
		}
	}
	s.CreatedAt = createdAt.Time
	s.UpdatedAt = updatedAt.Time
	return &s, nil
}

func (r *Repository) List(ctx context.Context) ([]domain.Session, error) {
	query := "SELECT id, issue_key, branch, repo_name, remote_host, worktree_path, working_directory, tmux_session, mutagen_sync_id, local_path, agent_name, status, tunnels_json, aws_profile, aws_config_json, created_at, updated_at FROM sessions ORDER BY updated_at DESC;"

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []domain.Session
	for rows.Next() {
		var s domain.Session
		var statusStr string
		var issueKey, branch, repoName, remoteHost, worktreePath, workingDir, tmuxSession, mutagenSyncID, localPath, agentName, tunnelsJSON, awsProfile, awsConfigJSON sql.NullString
		var createdAt, updatedAt sql.NullTime
		err := rows.Scan(&s.ID, &issueKey, &branch, &repoName, &remoteHost, &worktreePath, &workingDir, &tmuxSession, &mutagenSyncID, &localPath, &agentName, &statusStr, &tunnelsJSON, &awsProfile, &awsConfigJSON, &createdAt, &updatedAt)
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
		s.AWSProfileName = awsProfile.String
		if awsConfigJSON.Valid && awsConfigJSON.String != "" {
			var cfg domain.AWSConfig
			if err := json.Unmarshal([]byte(awsConfigJSON.String), &cfg); err == nil {
				s.AWSConfig = &cfg
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

// SaveSnapshot creates or replaces a session snapshot.
func (r *Repository) SaveSnapshot(ctx context.Context, s *domain.SessionSnapshot) error {
	stepsJSON, err := json.Marshal(s.NextSteps)
	if err != nil {
		return fmt.Errorf("failed to marshal next_steps: %w", err)
	}
	overviewJSON, _ := json.Marshal(s.Overview)
	detailsJSON, _ := json.Marshal(s.Details)
	actionsJSON, _ := json.Marshal(s.Actions)

	var injectedAt *time.Time
	if s.InjectedAt != nil {
		injectedAt = s.InjectedAt
	}
	query := `
	INSERT OR REPLACE INTO session_snapshots
		(id, session_id, issue_key, branch, repo_name, agent_name, worktree_path,
		 summary, overview_json, details_json, actions_json, next_steps_json,
		 agent_state, pane_content, injected_at, created_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`
	_, err = r.db.ExecContext(ctx, query,
		s.ID, s.SessionID, s.IssueKey, s.Branch, s.RepoName, s.AgentName, s.WorktreePath,
		s.Summary, string(overviewJSON), string(detailsJSON), string(actionsJSON), string(stepsJSON),
		string(s.AgentState), s.PaneContent, injectedAt, s.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to save snapshot: %w", err)
	}
	return nil
}

const snapshotSelectCols = `id, session_id, issue_key, branch, repo_name, agent_name, worktree_path,
	summary, overview_json, details_json, actions_json, next_steps_json,
	agent_state, pane_content, injected_at, created_at`

// GetLatestSnapshot returns the most recent snapshot for a session, or nil.
func (r *Repository) GetLatestSnapshot(ctx context.Context, sessionID string) (*domain.SessionSnapshot, error) {
	query := `SELECT ` + snapshotSelectCols + ` FROM session_snapshots WHERE session_id = ? ORDER BY created_at DESC LIMIT 1;`
	row := r.db.QueryRowContext(ctx, query, sessionID)
	s, err := scanSnapshot(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get latest snapshot: %w", err)
	}
	return s, nil
}

// ListSnapshots returns all snapshots for a session, newest first.
func (r *Repository) ListSnapshots(ctx context.Context, sessionID string) ([]domain.SessionSnapshot, error) {
	query := `SELECT ` + snapshotSelectCols + ` FROM session_snapshots WHERE session_id = ? ORDER BY created_at DESC;`
	rows, err := r.db.QueryContext(ctx, query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to list snapshots: %w", err)
	}
	defer rows.Close()
	return scanSnapshots(rows)
}

// ListAllSnapshots returns all snapshots across sessions, newest first.
func (r *Repository) ListAllSnapshots(ctx context.Context) ([]domain.SessionSnapshot, error) {
	query := `SELECT ` + snapshotSelectCols + ` FROM session_snapshots ORDER BY created_at DESC;`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list all snapshots: %w", err)
	}
	defer rows.Close()
	return scanSnapshots(rows)
}

// MarkSnapshotInjected records the time a snapshot was injected for resume.
func (r *Repository) MarkSnapshotInjected(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, "UPDATE session_snapshots SET injected_at = ? WHERE id = ?;", time.Now(), id)
	if err != nil {
		return fmt.Errorf("failed to mark snapshot injected: %w", err)
	}
	return nil
}

// DeleteSnapshot removes a snapshot by ID.
func (r *Repository) DeleteSnapshot(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM session_snapshots WHERE id = ?;", id)
	if err != nil {
		return fmt.Errorf("failed to delete snapshot: %w", err)
	}
	return nil
}

type snapshotScanner interface {
	Scan(dest ...any) error
}

func scanSnapshot(row snapshotScanner) (*domain.SessionSnapshot, error) {
	var s domain.SessionSnapshot
	var stepsJSON, overviewJSON, detailsJSON, actionsJSON string
	var injectedAt sql.NullTime
	var createdAt sql.NullTime
	err := row.Scan(
		&s.ID, &s.SessionID, &s.IssueKey, &s.Branch, &s.RepoName, &s.AgentName, &s.WorktreePath,
		&s.Summary, &overviewJSON, &detailsJSON, &actionsJSON, &stepsJSON,
		&s.AgentState, &s.PaneContent, &injectedAt, &createdAt,
	)
	if err != nil {
		return nil, err
	}
	if injectedAt.Valid {
		t := injectedAt.Time
		s.InjectedAt = &t
	}
	if createdAt.Valid {
		s.CreatedAt = createdAt.Time
	}
	unmarshalJSON := func(raw string, dest *[]string) {
		if raw != "" && raw != "null" {
			_ = json.Unmarshal([]byte(raw), dest)
		}
	}
	unmarshalJSON(overviewJSON, &s.Overview)
	unmarshalJSON(detailsJSON, &s.Details)
	unmarshalJSON(actionsJSON, &s.Actions)
	unmarshalJSON(stepsJSON, &s.NextSteps)
	return &s, nil
}

func scanSnapshots(rows *sql.Rows) ([]domain.SessionSnapshot, error) {
	var result []domain.SessionSnapshot
	for rows.Next() {
		s, err := scanSnapshot(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan snapshot: %w", err)
		}
		result = append(result, *s)
	}
	return result, rows.Err()
}

// ListSecrets returns all globally stored secrets.
func (r *Repository) ListSecrets(ctx context.Context) ([]domain.Secret, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT key, value, description FROM secrets ORDER BY key;")
	if err != nil {
		return nil, fmt.Errorf("failed to list secrets: %w", err)
	}
	defer rows.Close()
	var secrets []domain.Secret
	for rows.Next() {
		var s domain.Secret
		if err := rows.Scan(&s.Key, &s.Value, &s.Description); err != nil {
			return nil, fmt.Errorf("failed to scan secret: %w", err)
		}
		secrets = append(secrets, s)
	}
	return secrets, rows.Err()
}

// SaveSecret upserts a secret by key.
func (r *Repository) SaveSecret(ctx context.Context, s domain.Secret) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO secrets (key, value, description) VALUES (?, ?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value, description = excluded.description;",
		s.Key, s.Value, s.Description)
	if err != nil {
		return fmt.Errorf("failed to save secret: %w", err)
	}
	return nil
}

// DeleteSecret removes a secret by key.
func (r *Repository) DeleteSecret(ctx context.Context, key string) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM secrets WHERE key = ?;", key)
	if err != nil {
		return fmt.Errorf("failed to delete secret: %w", err)
	}
	return nil
}
