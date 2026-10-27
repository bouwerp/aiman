package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// resetPRWatchStmts clear every table holding per-session CI watcher state.
// A new CI table needs a statement here too.
var resetPRWatchStmts = []string{
	`DELETE FROM ci_watch WHERE session_id=?`,
	`DELETE FROM ci_check_failures WHERE session_id=?`,
	`DELETE FROM ci_escalations WHERE session_id=?`,
	`DELETE FROM ci_seen_comments WHERE session_id=?`,
}

var clearCheckStmts = []string{
	`DELETE FROM ci_check_failures WHERE session_id=? AND check_name=?`,
	`DELETE FROM ci_escalations WHERE session_id=? AND check_name=?`,
}

func ensureCITables(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS ci_watch(
			session_id TEXT PRIMARY KEY,
			pr_number INTEGER NOT NULL DEFAULT 0,
			pr_state TEXT NOT NULL DEFAULT '',
			head_sha TEXT NOT NULL DEFAULT '',
			watch_since DATETIME,
			last_polled_at DATETIME,
			last_error TEXT NOT NULL DEFAULT ''
		);`,
		`CREATE TABLE IF NOT EXISTS ci_check_failures(
			session_id TEXT NOT NULL,
			check_name TEXT NOT NULL,
			head_sha TEXT NOT NULL,
			failed_at DATETIME NOT NULL,
			PRIMARY KEY(session_id, check_name, head_sha)
		);`,
		`CREATE TABLE IF NOT EXISTS ci_escalations(
			session_id TEXT NOT NULL,
			check_name TEXT NOT NULL,
			escalated_at DATETIME NOT NULL,
			PRIMARY KEY(session_id, check_name)
		);`,
		`CREATE TABLE IF NOT EXISTS ci_seen_comments(
			session_id TEXT NOT NULL,
			comment_id INTEGER NOT NULL,
			PRIMARY KEY(session_id, comment_id)
		);`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// LoadPRWatch returns the watcher state for a session. A session never
// watched yields the zero state with SessionID set.
func (r *Repository) LoadPRWatch(ctx context.Context, sessionID string) (domain.PRWatchState, error) {
	st := domain.PRWatchState{SessionID: sessionID}
	var since, polled sql.NullTime
	err := r.db.QueryRowContext(ctx,
		`SELECT pr_number, pr_state, head_sha, watch_since, last_polled_at, last_error FROM ci_watch WHERE session_id=?`, sessionID).
		Scan(&st.PRNumber, &st.PRState, &st.HeadSHA, &since, &polled, &st.LastError)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return st, err
	}
	if since.Valid {
		st.WatchSince = since.Time
	}
	if polled.Valid {
		st.LastPolledAt = polled.Time
	}

	rows, err := r.db.QueryContext(ctx, `SELECT check_name FROM ci_escalations WHERE session_id=?`, sessionID)
	if err != nil {
		return st, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return st, err
		}
		st.Escalated = append(st.Escalated, name)
	}
	if err := rows.Err(); err != nil {
		return st, err
	}
	sort.Strings(st.Escalated)
	return st, nil
}

// SavePRWatch upserts the watcher state. Escalated is ignored: escalations
// are written only by MarkCheckEscalated.
func (r *Repository) SavePRWatch(ctx context.Context, st domain.PRWatchState) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO ci_watch(session_id, pr_number, pr_state, head_sha, watch_since, last_polled_at, last_error)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(session_id) DO UPDATE SET
			pr_number=excluded.pr_number,
			pr_state=excluded.pr_state,
			head_sha=excluded.head_sha,
			watch_since=excluded.watch_since,
			last_polled_at=excluded.last_polled_at,
			last_error=excluded.last_error`,
		st.SessionID, st.PRNumber, st.PRState, st.HeadSHA, nullTime(st.WatchSince), nullTime(st.LastPolledAt), st.LastError)
	return err
}

// RecordCheckFailure records that check failed at sha and returns how many
// distinct SHAs it has failed on. Re-polling the same SHA does not count as
// another failed fix.
func (r *Repository) RecordCheckFailure(ctx context.Context, sessionID, check, sha string) (int, error) {
	if _, err := r.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO ci_check_failures(session_id, check_name, head_sha, failed_at) VALUES(?,?,?,?)`,
		sessionID, check, sha, time.Now().UTC()); err != nil {
		return 0, err
	}
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM ci_check_failures WHERE session_id=? AND check_name=?`, sessionID, check).Scan(&n)
	return n, err
}

// MarkCheckEscalated records an escalation and reports whether it is new, so
// a check is escalated once until it is cleared.
func (r *Repository) MarkCheckEscalated(ctx context.Context, sessionID, check string, at time.Time) (bool, error) {
	res, err := r.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO ci_escalations(session_id, check_name, escalated_at) VALUES(?,?,?)`,
		sessionID, check, at.UTC())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ClearCheck forgets a check's failures and escalation once it passes.
func (r *Repository) ClearCheck(ctx context.Context, sessionID, check string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range clearCheckStmts {
		if _, err := tx.ExecContext(ctx, stmt, sessionID, check); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SeenComments returns the review comment ids already handled for a session.
func (r *Repository) SeenComments(ctx context.Context, sessionID string) (map[int64]bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT comment_id FROM ci_seen_comments WHERE session_id=?`, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	seen := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		seen[id] = true
	}
	return seen, rows.Err()
}

// MarkCommentsSeen records comment ids as handled, all or none.
func (r *Repository) MarkCommentsSeen(ctx context.Context, sessionID string, ids []int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO ci_seen_comments(session_id, comment_id) VALUES(?,?)`, sessionID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ResetPRWatch deletes every CI row for a session.
func (r *Repository) ResetPRWatch(ctx context.Context, sessionID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range resetPRWatchStmts {
		if _, err := tx.ExecContext(ctx, stmt, sessionID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// nullTime stores a zero time as NULL so it reads back as zero.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}
