package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ensureForgottenSessions creates the tombstones of sessions serve ended on
// purpose. session.register reads them so a client still holding a copy of
// an ended session cannot put its row back.
func ensureForgottenSessions(db *sql.DB) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS forgotten_sessions (
	id TEXT PRIMARY KEY,
	at DATETIME NOT NULL,
	reason TEXT NOT NULL DEFAULT ''
);`
	_, err := db.ExecContext(context.Background(), ddl)
	return err
}

// MarkForgotten records that session id was ended deliberately. A later mark
// replaces the time and reason.
func (r *Repository) MarkForgotten(ctx context.Context, id, reason string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO forgotten_sessions (id, at, reason) VALUES (?, ?, ?)
ON CONFLICT(id) DO UPDATE SET at = excluded.at, reason = excluded.reason;`, id, time.Now().UTC(), reason)
	if err != nil {
		return fmt.Errorf("marking session forgotten: %w", err)
	}
	return nil
}

// IsForgotten reports whether session id holds a tombstone.
func (r *Repository) IsForgotten(ctx context.Context, id string) (bool, error) {
	var one int
	err := r.rd.QueryRowContext(ctx, `SELECT 1 FROM forgotten_sessions WHERE id = ?;`, strings.TrimSpace(id)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading forgotten session: %w", err)
	}
	return true, nil
}

// ClearForgotten removes session id's tombstone, if any.
func (r *Repository) ClearForgotten(ctx context.Context, id string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM forgotten_sessions WHERE id = ?;`, strings.TrimSpace(id)); err != nil {
		return fmt.Errorf("clearing forgotten session: %w", err)
	}
	return nil
}
