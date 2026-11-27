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
// Watch state, failures and escalations are keyed by (session_id, repo); a
// plain session uses repo "". Seen review comments stay keyed per session.
var resetPRWatchStmts = []string{
	`DELETE FROM ci_watch WHERE session_id=?`,
	`DELETE FROM ci_check_failures WHERE session_id=?`,
	`DELETE FROM ci_escalations WHERE session_id=?`,
	`DELETE FROM ci_seen_comments WHERE session_id=?`,
}

var clearCheckStmts = []string{
	`DELETE FROM ci_check_failures WHERE session_id=? AND repo=? AND check_name=?`,
	`DELETE FROM ci_escalations WHERE session_id=? AND repo=? AND check_name=?`,
}

// ciTables are the CI watcher tables as they are now.
var ciTables = []struct{ name, create, copyCols string }{
	{"ci_watch", `CREATE TABLE IF NOT EXISTS ci_watch(
			session_id TEXT NOT NULL,
			repo TEXT NOT NULL DEFAULT '',
			pr_number INTEGER NOT NULL DEFAULT 0,
			pr_state TEXT NOT NULL DEFAULT '',
			head_sha TEXT NOT NULL DEFAULT '',
			watch_since DATETIME,
			last_polled_at DATETIME,
			last_error TEXT NOT NULL DEFAULT '',
			PRIMARY KEY(session_id, repo)
		);`, "session_id, pr_number, pr_state, head_sha, watch_since, last_polled_at, last_error"},
	{"ci_check_failures", `CREATE TABLE IF NOT EXISTS ci_check_failures(
			session_id TEXT NOT NULL,
			repo TEXT NOT NULL DEFAULT '',
			check_name TEXT NOT NULL,
			head_sha TEXT NOT NULL,
			failed_at DATETIME NOT NULL,
			PRIMARY KEY(session_id, repo, check_name, head_sha)
		);`, "session_id, check_name, head_sha, failed_at"},
	{"ci_escalations", `CREATE TABLE IF NOT EXISTS ci_escalations(
			session_id TEXT NOT NULL,
			repo TEXT NOT NULL DEFAULT '',
			check_name TEXT NOT NULL,
			escalated_at DATETIME NOT NULL,
			PRIMARY KEY(session_id, repo, check_name)
		);`, "session_id, check_name, escalated_at"},
}

func ensureCITables(db *sql.DB) error {
	for _, t := range ciTables {
		if err := ensureRepoKeyed(db, t.name, t.create, t.copyCols); err != nil {
			return err
		}
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS ci_seen_comments(
			session_id TEXT NOT NULL,
			comment_id INTEGER NOT NULL,
			PRIMARY KEY(session_id, comment_id)
		);`)
	return err
}

// ensureRepoKeyed creates a CI table, or rebuilds one written before the repo
// key existed: its rows are copied with repo "", the plain session's key. The
// primary key changes, so ALTER TABLE cannot do it.
func ensureRepoKeyed(db *sql.DB, name, create, copyCols string) error {
	exists, hasRepo, err := tableHasColumn(db, name, "repo")
	if err != nil {
		return err
	}
	if !exists {
		_, err := db.Exec(create)
		return err
	}
	if hasRepo {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	old := name + "_old"
	for _, stmt := range []string{
		`ALTER TABLE ` + name + ` RENAME TO ` + old,
		create,
		`INSERT INTO ` + name + `(` + copyCols + `) SELECT ` + copyCols + ` FROM ` + old,
		`DROP TABLE ` + old,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// tableHasColumn reports whether a table exists and whether it has col.
func tableHasColumn(db *sql.DB, table, col string) (exists, has bool, err error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return false, false, err
		}
		exists = true
		if n == col {
			has = true
		}
	}
	return exists, has, rows.Err()
}

// LoadPRWatch returns the watcher state of a plain session (repo "").
func (r *Repository) LoadPRWatch(ctx context.Context, sessionID string) (domain.PRWatchState, error) {
	return r.LoadPRWatchRepo(ctx, sessionID, "")
}

// LoadPRWatchRepo returns the watcher state for one repo of a session. A repo
// never watched yields the zero state with SessionID and Repo set.
func (r *Repository) LoadPRWatchRepo(ctx context.Context, sessionID, repo string) (domain.PRWatchState, error) {
	st := domain.PRWatchState{SessionID: sessionID, Repo: repo}
	var since, polled sql.NullTime
	err := r.rd.QueryRowContext(ctx,
		`SELECT pr_number, pr_state, head_sha, watch_since, last_polled_at, last_error FROM ci_watch WHERE session_id=? AND repo=?`, sessionID, repo).
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

	rows, err := r.rd.QueryContext(ctx, `SELECT check_name FROM ci_escalations WHERE session_id=? AND repo=?`, sessionID, repo)
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

// SavePRWatch upserts the watcher state of (st.SessionID, st.Repo). Escalated is ignored: escalations
// are written only by MarkCheckEscalated.
func (r *Repository) SavePRWatch(ctx context.Context, st domain.PRWatchState) error {
	_, err := r.exec(ctx, `
		INSERT INTO ci_watch(session_id, repo, pr_number, pr_state, head_sha, watch_since, last_polled_at, last_error)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(session_id, repo) DO UPDATE SET
			pr_number=excluded.pr_number,
			pr_state=excluded.pr_state,
			head_sha=excluded.head_sha,
			watch_since=excluded.watch_since,
			last_polled_at=excluded.last_polled_at,
			last_error=excluded.last_error`,
		st.SessionID, st.Repo, st.PRNumber, st.PRState, st.HeadSHA, nullTime(st.WatchSince), nullTime(st.LastPolledAt), st.LastError)
	return err
}

// RecordCheckFailure is RecordCheckFailureRepo for a plain session.
func (r *Repository) RecordCheckFailure(ctx context.Context, sessionID, check, sha string) (int, error) {
	return r.RecordCheckFailureRepo(ctx, sessionID, "", check, sha)
}

// RecordCheckFailureRepo records that check failed at sha in one repo of a
// session and returns how many distinct SHAs it has failed on there.
// Re-polling the same SHA does not count as another failed fix.
func (r *Repository) RecordCheckFailureRepo(ctx context.Context, sessionID, repo, check, sha string) (int, error) {
	if _, err := r.exec(ctx,
		`INSERT OR IGNORE INTO ci_check_failures(session_id, repo, check_name, head_sha, failed_at) VALUES(?,?,?,?,?)`,
		sessionID, repo, check, sha, time.Now().UTC()); err != nil {
		return 0, err
	}
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM ci_check_failures WHERE session_id=? AND repo=? AND check_name=?`, sessionID, repo, check).Scan(&n)
	return n, err
}

// MarkCheckEscalated is MarkCheckEscalatedRepo for a plain session.
func (r *Repository) MarkCheckEscalated(ctx context.Context, sessionID, check string, at time.Time) (bool, error) {
	return r.MarkCheckEscalatedRepo(ctx, sessionID, "", check, at)
}

// MarkCheckEscalatedRepo records an escalation and reports whether it is new,
// so a check of one repo is escalated once until it is cleared.
func (r *Repository) MarkCheckEscalatedRepo(ctx context.Context, sessionID, repo, check string, at time.Time) (bool, error) {
	res, err := r.exec(ctx,
		`INSERT OR IGNORE INTO ci_escalations(session_id, repo, check_name, escalated_at) VALUES(?,?,?,?)`,
		sessionID, repo, check, at.UTC())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ClearCheck is ClearCheckRepo for a plain session.
func (r *Repository) ClearCheck(ctx context.Context, sessionID, check string) error {
	return r.ClearCheckRepo(ctx, sessionID, "", check)
}

// ClearCheckRepo forgets a check's failures and escalation in one repo once
// it passes.
func (r *Repository) ClearCheckRepo(ctx context.Context, sessionID, repo, check string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range clearCheckStmts {
		if _, err := tx.ExecContext(ctx, stmt, sessionID, repo, check); err != nil {
			return err
		}
	}
	return r.commit(tx)
}

// SeenComments returns the review comment ids already handled for a session.
func (r *Repository) SeenComments(ctx context.Context, sessionID string) (map[int64]bool, error) {
	rows, err := r.rd.QueryContext(ctx, `SELECT comment_id FROM ci_seen_comments WHERE session_id=?`, sessionID)
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
	return r.commit(tx)
}

// ResetPRWatch deletes every CI row for a session, every repo included.
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
	return r.commit(tx)
}

// nullTime stores a zero time as NULL so it reads back as zero.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}
