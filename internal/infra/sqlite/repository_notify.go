package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func ensureNotifyTables(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS notifications(
			seq INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			label TEXT NOT NULL,
			host TEXT NOT NULL,
			text TEXT NOT NULL,
			created_at DATETIME NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS notify_last(
			session_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			notified_at DATETIME NOT NULL,
			PRIMARY KEY(session_id, kind)
		);`,
		`CREATE TABLE IF NOT EXISTS notify_cursors(
			name TEXT PRIMARY KEY,
			seq INTEGER NOT NULL
		);`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// AppendIfDue appends a notification if minInterval has passed for this session/kind.
func (r *Repository) AppendIfDue(ctx context.Context, n domain.Notification, minInterval time.Duration) (domain.Notification, bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Notification{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var last sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT notified_at FROM notify_last WHERE session_id=? AND kind=?`, n.SessionID, string(n.Kind)).Scan(&last); err != nil && err != sql.ErrNoRows {
		return domain.Notification{}, false, err
	}
	if last.Valid && !n.At.IsZero() && n.At.Sub(last.Time) < minInterval {
		return domain.Notification{}, false, nil
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO notifications(session_id,kind,label,host,text,created_at) VALUES(?,?,?,?,?,?)`,
		n.SessionID, string(n.Kind), n.Label, n.Host, n.Text, n.At.UTC())
	if err != nil {
		return domain.Notification{}, false, err
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return domain.Notification{}, false, err
	}
	n.Seq = seq
	if _, err := tx.ExecContext(ctx, `INSERT INTO notify_last(session_id,kind,notified_at) VALUES(?,?,?) ON CONFLICT(session_id,kind) DO UPDATE SET notified_at=excluded.notified_at`,
		n.SessionID, string(n.Kind), n.At.UTC()); err != nil {
		return domain.Notification{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Notification{}, false, err
	}
	return n, true, nil
}

// NotificationsSince returns notifications with seq > after.
func (r *Repository) NotificationsSince(ctx context.Context, after int64, limit int) ([]domain.Notification, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `SELECT seq,session_id,kind,label,host,text,created_at FROM notifications WHERE seq > ? ORDER BY seq ASC LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Notification
	for rows.Next() {
		var n domain.Notification
		var kind string
		var at time.Time
		if err := rows.Scan(&n.Seq, &n.SessionID, &kind, &n.Label, &n.Host, &n.Text, &at); err != nil {
			return nil, err
		}
		n.Kind = domain.NotifyKind(kind)
		n.At = at.UTC()
		out = append(out, n)
	}
	return out, rows.Err()
}

// LastNotificationSeq returns the highest seq in notifications.
func (r *Repository) LastNotificationSeq(ctx context.Context) (int64, error) {
	var seq sql.NullInt64
	if err := r.db.QueryRowContext(ctx, `SELECT MAX(seq) FROM notifications`).Scan(&seq); err != nil {
		return 0, err
	}
	if !seq.Valid {
		return 0, nil
	}
	return seq.Int64, nil
}

// NotifyCursor loads a named cursor.
func (r *Repository) NotifyCursor(ctx context.Context, name string) (int64, bool, error) {
	var seq int64
	err := r.db.QueryRowContext(ctx, `SELECT seq FROM notify_cursors WHERE name=?`, name).Scan(&seq)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return seq, true, nil
}

// SetNotifyCursor stores a named cursor.
func (r *Repository) SetNotifyCursor(ctx context.Context, name string, seq int64) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO notify_cursors(name,seq) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET seq=excluded.seq`, name, seq)
	return err
}

// PruneNotifications deletes notifications older than before.
func (r *Repository) PruneNotifications(ctx context.Context, before time.Time) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM notifications WHERE created_at < ?`, before.UTC())
	return err
}
