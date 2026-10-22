package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// AddSteerNote queues a steering note and sets n.ID. Times are stored in UTC
// so MAX(delivered_at) orders correctly as text.
func (r *Repository) AddSteerNote(ctx context.Context, n *domain.SteerRecord) error {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO steer_notes (session_id, author, text, created_at) VALUES (?, ?, ?, ?)`,
		n.SessionID, string(n.Author), n.Text, n.CreatedAt.UTC())
	if err != nil {
		return fmt.Errorf("failed to add steer note: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed to read steer note id: %w", err)
	}
	n.ID = id
	return nil
}

// ListUndeliveredSteerNotes returns every note not yet delivered, oldest first.
func (r *Repository) ListUndeliveredSteerNotes(ctx context.Context) ([]domain.SteerRecord, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, session_id, author, text, created_at FROM steer_notes WHERE delivered_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("failed to list steer notes: %w", err)
	}
	defer rows.Close()
	var out []domain.SteerRecord
	for rows.Next() {
		var n domain.SteerRecord
		var author string
		if err := rows.Scan(&n.ID, &n.SessionID, &author, &n.Text, &n.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan steer note: %w", err)
		}
		n.Author = domain.TodoAuthor(author)
		out = append(out, n)
	}
	return out, rows.Err()
}

// MarkSteerNotesDelivered stamps the given notes as delivered at the given time.
func (r *Repository) MarkSteerNotesDelivered(ctx context.Context, ids []int64, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to mark steer notes delivered: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE steer_notes SET delivered_at = ? WHERE id = ?`, at.UTC(), id); err != nil {
			return fmt.Errorf("failed to mark steer notes delivered: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to mark steer notes delivered: %w", err)
	}
	return nil
}

// DeleteSteerNotes removes every note for a session.
func (r *Repository) DeleteSteerNotes(ctx context.Context, sessionID string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM steer_notes WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("failed to delete steer notes: %w", err)
	}
	return nil
}

// LastSteerDelivery returns the latest delivery time per session. Sessions
// with no delivered notes are absent.
func (r *Repository) LastSteerDelivery(ctx context.Context) (map[string]time.Time, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT session_id, MAX(delivered_at) FROM steer_notes WHERE delivered_at IS NOT NULL GROUP BY session_id`)
	if err != nil {
		return nil, fmt.Errorf("failed to query steer deliveries: %w", err)
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var id string
		var raw sql.NullString
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, fmt.Errorf("failed to scan steer delivery: %w", err)
		}
		if !raw.Valid {
			continue
		}
		t, err := parseStoredTime(raw.String)
		if err != nil {
			return nil, err
		}
		out[id] = t
	}
	return out, rows.Err()
}

// parseStoredTime reads the text forms the driver writes for time.Time values.
func parseStoredTime(s string) (time.Time, error) {
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05.999999999-07:00",
		time.RFC3339Nano,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised stored time %q", s)
}
