package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/bouwerp/aiman/internal/domain"
)

func ensureSessionRepos(db *sql.DB) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS session_repos (
	session_id TEXT NOT NULL,
	ord INTEGER NOT NULL DEFAULT 0,
	repo TEXT NOT NULL,
	branch TEXT NOT NULL DEFAULT '',
	base_branch TEXT NOT NULL DEFAULT '',
	worktree_path TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (session_id, repo)
);`
	_, err := db.ExecContext(context.Background(), ddl)
	return err
}

// SetSessionRepos replaces the member set of a workspace session in one
// transaction. Save never touches these rows.
func (r *Repository) SetSessionRepos(ctx context.Context, sessionID string, members []domain.SessionRepo) error {
	err := func() error {
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(ctx, `DELETE FROM session_repos WHERE session_id = ?;`, sessionID); err != nil {
			return err
		}
		for _, m := range members {
			if _, err := tx.ExecContext(ctx, `INSERT INTO session_repos (session_id, ord, repo, branch, base_branch, worktree_path) VALUES (?, ?, ?, ?, ?, ?);`,
				sessionID, m.Ord, m.Repo, m.Branch, m.BaseBranch, m.WorktreePath); err != nil {
				return err
			}
		}
		return tx.Commit()
	}()
	if err != nil {
		return fmt.Errorf("setting session repos: %w", err)
	}
	return nil
}

// loadMembers returns the members of the given sessions keyed by session id;
// an empty ids slice loads every session's.
func (r *Repository) loadMembers(ctx context.Context, ids ...string) (map[string][]domain.SessionRepo, error) {
	q := `SELECT session_id, ord, repo, branch, base_branch, worktree_path FROM session_repos`
	var args []any
	if len(ids) == 1 {
		q += ` WHERE session_id = ?`
		args = append(args, ids[0])
	}
	rows, err := r.rd.QueryContext(ctx, q+` ORDER BY session_id, ord, repo;`, args...)
	if err != nil {
		return nil, fmt.Errorf("loading session repos: %w", err)
	}
	defer rows.Close()
	out := map[string][]domain.SessionRepo{}
	for rows.Next() {
		var id string
		var m domain.SessionRepo
		if err := rows.Scan(&id, &m.Ord, &m.Repo, &m.Branch, &m.BaseBranch, &m.WorktreePath); err != nil {
			return nil, err
		}
		out[id] = append(out[id], m)
	}
	return out, rows.Err()
}
