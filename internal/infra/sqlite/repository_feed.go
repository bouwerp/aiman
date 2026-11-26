package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

const feedHighWaterKey = "high_water"

func ensureFeedTables(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS feed_batches(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			event_ids TEXT NOT NULL,
			up_to_event_id INTEGER NOT NULL,
			state TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			agent TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			summary TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			started_at INTEGER NOT NULL,
			ended_at INTEGER NOT NULL DEFAULT 0
		);`,
		`CREATE INDEX IF NOT EXISTS idx_feed_batches_state ON feed_batches(state);`,
		`CREATE TABLE IF NOT EXISTS feed_actions(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			batch_id INTEGER NOT NULL,
			kind TEXT NOT NULL,
			rationale TEXT NOT NULL DEFAULT '',
			priority TEXT NOT NULL,
			answers TEXT NOT NULL,
			options TEXT NOT NULL,
			trace TEXT NOT NULL,
			state TEXT NOT NULL,
			chosen INTEGER,
			chosen_params TEXT NOT NULL DEFAULT '',
			result TEXT NOT NULL DEFAULT '',
			decided_by TEXT NOT NULL DEFAULT '',
			dismissal TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			decided_at INTEGER NOT NULL DEFAULT 0,
			expires_at INTEGER NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_feed_actions_state ON feed_actions(state);`,
		`CREATE TABLE IF NOT EXISTS feed_holds(
			thread_key TEXT PRIMARY KEY,
			until_event_id INTEGER NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS feed_state(
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);`,
		// feed_digests holds the digest events FoldDigests makes. id is negative,
		// from the manager's own sequence; members is the JSON array of the
		// folded feed.db event ids and event the JSON of the digest FeedEvent.
		`CREATE TABLE IF NOT EXISTS feed_digests(
			id INTEGER PRIMARY KEY,
			members TEXT NOT NULL,
			event TEXT NOT NULL,
			created_at INTEGER NOT NULL
		);`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

func feedMS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func feedTime(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

func feedJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// CutFeedBatch inserts the batch and sets high_water to b.UpToEventID in one
// transaction. It returns the batch with its id.
func (r *Repository) CutFeedBatch(ctx context.Context, b domain.FeedBatch) (domain.FeedBatch, error) {
	ids, err := feedJSON(nonNilInts(b.EventIDs))
	if err != nil {
		return b, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return b, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `INSERT INTO feed_batches(event_ids, up_to_event_id, state, attempts, agent, model, summary, error, started_at, ended_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, ids, b.UpToEventID, string(b.State), b.Attempts, b.Agent, b.Model, b.Summary, b.Error,
		feedMS(b.StartedAt), feedMS(b.EndedAt))
	if err != nil {
		return b, err
	}
	if b.ID, err = res.LastInsertId(); err != nil {
		return b, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO feed_state(key, value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, feedHighWaterKey, strconv.FormatInt(b.UpToEventID, 10)); err != nil {
		return b, err
	}
	if err := tx.Commit(); err != nil {
		return b, err
	}
	r.changed(ChangeOther)
	return b, nil
}

func nonNilInts(v []int64) []int64 {
	if v == nil {
		return []int64{}
	}
	return v
}

// FeedHighWater is the newest event id any batch has cut, 0 before the first.
func (r *Repository) FeedHighWater(ctx context.Context) (int64, error) {
	var v string
	err := r.rd.QueryRowContext(ctx, `SELECT value FROM feed_state WHERE key = ?`, feedHighWaterKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

// UpdateFeedBatch rewrites a batch's mutable fields.
func (r *Repository) UpdateFeedBatch(ctx context.Context, b domain.FeedBatch) error {
	_, err := r.db.ExecContext(ctx, `UPDATE feed_batches SET state=?, attempts=?, agent=?, model=?, summary=?, error=?, started_at=?, ended_at=? WHERE id=?`,
		string(b.State), b.Attempts, b.Agent, b.Model, b.Summary, b.Error, feedMS(b.StartedAt), feedMS(b.EndedAt), b.ID)
	if err == nil {
		r.changed(ChangeOther)
	}
	return err
}

const feedBatchCols = `id, event_ids, up_to_event_id, state, attempts, agent, model, summary, error, started_at, ended_at`

type feedScanner interface{ Scan(dest ...any) error }

func scanFeedBatch(s feedScanner) (domain.FeedBatch, error) {
	var b domain.FeedBatch
	var ids, state string
	var started, ended int64
	if err := s.Scan(&b.ID, &ids, &b.UpToEventID, &state, &b.Attempts, &b.Agent, &b.Model, &b.Summary, &b.Error, &started, &ended); err != nil {
		return b, err
	}
	if err := json.Unmarshal([]byte(ids), &b.EventIDs); err != nil {
		return b, err
	}
	b.State = domain.FeedBatchState(state)
	b.StartedAt, b.EndedAt = feedTime(started), feedTime(ended)
	return b, nil
}

// GetFeedBatch reads one batch; a missing id is sql.ErrNoRows.
func (r *Repository) GetFeedBatch(ctx context.Context, id int64) (domain.FeedBatch, error) {
	return scanFeedBatch(r.rd.QueryRowContext(ctx, `SELECT `+feedBatchCols+` FROM feed_batches WHERE id = ?`, id))
}

func (r *Repository) queryFeedBatches(ctx context.Context, query string, args ...any) ([]domain.FeedBatch, error) {
	rows, err := r.rd.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.FeedBatch
	for rows.Next() {
		b, err := scanFeedBatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ListFeedBatches lists batches newest first.
func (r *Repository) ListFeedBatches(ctx context.Context, limit int) ([]domain.FeedBatch, error) {
	return r.queryFeedBatches(ctx, `SELECT `+feedBatchCols+` FROM feed_batches ORDER BY id DESC LIMIT ?`, limit)
}

// RunningFeedBatches lists batches still in state running, oldest first.
func (r *Repository) RunningFeedBatches(ctx context.Context) ([]domain.FeedBatch, error) {
	return r.queryFeedBatches(ctx, `SELECT `+feedBatchCols+` FROM feed_batches WHERE state = 'running' ORDER BY id ASC`)
}

// DailyFeedBatches counts the batches started at or after since.
func (r *Repository) DailyFeedBatches(ctx context.Context, since time.Time) (int, error) {
	var n int
	err := r.rd.QueryRowContext(ctx, `SELECT COUNT(*) FROM feed_batches WHERE started_at >= ?`, feedMS(since)).Scan(&n)
	return n, err
}

type feedActionRow struct {
	answers, options, trace, dismissal string
	chosen                             any
}

func encodeFeedAction(a domain.FeedAction) (feedActionRow, error) {
	var row feedActionRow
	var err error
	if row.answers, err = feedJSON(nonNilInts(a.Answers)); err != nil {
		return row, err
	}
	opts := a.Options
	if opts == nil {
		opts = []domain.FeedOption{}
	}
	if row.options, err = feedJSON(opts); err != nil {
		return row, err
	}
	trace := a.Trace
	if trace == nil {
		trace = []domain.FeedTraceEntry{}
	}
	if row.trace, err = feedJSON(trace); err != nil {
		return row, err
	}
	if a.Dismissal != nil {
		if row.dismissal, err = feedJSON(a.Dismissal); err != nil {
			return row, err
		}
	}
	if a.Chosen != nil {
		row.chosen = *a.Chosen
	}
	return row, nil
}

// InsertFeedAction stores a new action and returns it with its id.
func (r *Repository) InsertFeedAction(ctx context.Context, a domain.FeedAction) (domain.FeedAction, error) {
	row, err := encodeFeedAction(a)
	if err != nil {
		return a, err
	}
	res, err := r.db.ExecContext(ctx, `INSERT INTO feed_actions(batch_id, kind, rationale, priority, answers, options, trace, state, chosen, chosen_params, result, decided_by, dismissal, created_at, decided_at, expires_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.BatchID, string(a.Kind), a.Rationale, string(a.Priority), row.answers, row.options, row.trace, string(a.State),
		row.chosen, string(a.ChosenParams), a.Result, a.DecidedBy, row.dismissal, feedMS(a.CreatedAt), feedMS(a.DecidedAt), feedMS(a.ExpiresAt))
	if err != nil {
		return a, err
	}
	if a.ID, err = res.LastInsertId(); err != nil {
		return a, err
	}
	r.changed(ChangeOther)
	return a, nil
}

// UpdateFeedAction rewrites an action. Its batch and creation time never change.
func (r *Repository) UpdateFeedAction(ctx context.Context, a domain.FeedAction) error {
	row, err := encodeFeedAction(a)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `UPDATE feed_actions SET kind=?, rationale=?, priority=?, answers=?, options=?, trace=?, state=?, chosen=?, chosen_params=?, result=?, decided_by=?, dismissal=?, decided_at=?, expires_at=? WHERE id=?`,
		string(a.Kind), a.Rationale, string(a.Priority), row.answers, row.options, row.trace, string(a.State), row.chosen,
		string(a.ChosenParams), a.Result, a.DecidedBy, row.dismissal, feedMS(a.DecidedAt), feedMS(a.ExpiresAt), a.ID)
	if err == nil {
		r.changed(ChangeOther)
	}
	return err
}

const feedActionCols = `id, batch_id, kind, rationale, priority, answers, options, trace, state, chosen, chosen_params, result, decided_by, dismissal, created_at, decided_at, expires_at`

func scanFeedAction(s feedScanner) (domain.FeedAction, error) {
	var a domain.FeedAction
	var kind, prio, answers, options, trace, state, params, dismissal string
	var chosen sql.NullInt64
	var created, decided, expires int64
	if err := s.Scan(&a.ID, &a.BatchID, &kind, &a.Rationale, &prio, &answers, &options, &trace, &state, &chosen, &params,
		&a.Result, &a.DecidedBy, &dismissal, &created, &decided, &expires); err != nil {
		return a, err
	}
	a.Kind, a.Priority, a.State = domain.FeedActionKind(kind), domain.FeedPriority(prio), domain.FeedActionState(state)
	if err := json.Unmarshal([]byte(answers), &a.Answers); err != nil {
		return a, err
	}
	if err := json.Unmarshal([]byte(options), &a.Options); err != nil {
		return a, err
	}
	if err := json.Unmarshal([]byte(trace), &a.Trace); err != nil {
		return a, err
	}
	if chosen.Valid {
		c := int(chosen.Int64)
		a.Chosen = &c
	}
	if params != "" {
		a.ChosenParams = json.RawMessage(params)
	}
	if dismissal != "" {
		a.Dismissal = &domain.FeedDismissal{}
		if err := json.Unmarshal([]byte(dismissal), a.Dismissal); err != nil {
			return a, err
		}
	}
	a.CreatedAt, a.DecidedAt, a.ExpiresAt = feedTime(created), feedTime(decided), feedTime(expires)
	return a, nil
}

// GetFeedAction reads one action; a missing id is sql.ErrNoRows.
func (r *Repository) GetFeedAction(ctx context.Context, id int64) (domain.FeedAction, error) {
	return scanFeedAction(r.rd.QueryRowContext(ctx, `SELECT `+feedActionCols+` FROM feed_actions WHERE id = ?`, id))
}

func (r *Repository) queryFeedActions(ctx context.Context, query string, args ...any) ([]domain.FeedAction, error) {
	rows, err := r.rd.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.FeedAction
	for rows.Next() {
		a, err := scanFeedAction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListFeedActions lists actions in the given states (every state when none is
// given): priority high first, then newest.
func (r *Repository) ListFeedActions(ctx context.Context, states []domain.FeedActionState, limit int) ([]domain.FeedAction, error) {
	q := `SELECT ` + feedActionCols + ` FROM feed_actions`
	var args []any
	if len(states) > 0 {
		q += ` WHERE state IN (` + strings.TrimSuffix(strings.Repeat("?,", len(states)), ",") + `)`
		for _, s := range states {
			args = append(args, string(s))
		}
	}
	q += ` ORDER BY CASE priority WHEN 'high' THEN 0 WHEN 'normal' THEN 1 ELSE 2 END, created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	return r.queryFeedActions(ctx, q, args...)
}

// OpenFeedActionEventFloor is the smallest event id named by the answers or
// event trace refs of an open action (proposed, approved, running or snoozed),
// 0 when there is none. The prune floor never rises past it.
func (r *Repository) OpenFeedActionEventFloor(ctx context.Context) (int64, error) {
	open, err := r.queryFeedActions(ctx, `SELECT `+feedActionCols+` FROM feed_actions WHERE state IN ('proposed','approved','running','snoozed')`)
	if err != nil {
		return 0, err
	}
	var floor int64
	note := func(id int64) {
		if id > 0 && (floor == 0 || id < floor) {
			floor = id
		}
	}
	for _, a := range open {
		for _, id := range a.Answers {
			note(id)
		}
		for _, t := range a.Trace {
			if t.Kind != "event" {
				continue
			}
			if id, err := strconv.ParseInt(t.Ref, 10, 64); err == nil {
				note(id)
			}
		}
	}
	return floor, nil
}

// HoldFeedThread holds a thread's events up to and including untilEventID out
// of batches. A second hold on the same thread replaces the first.
func (r *Repository) HoldFeedThread(ctx context.Context, threadKey string, untilEventID int64) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO feed_holds(thread_key, until_event_id) VALUES(?,?)
		ON CONFLICT(thread_key) DO UPDATE SET until_event_id = excluded.until_event_id`, threadKey, untilEventID)
	if err == nil {
		r.changed(ChangeOther)
	}
	return err
}

// FeedThreadHeld reports whether eventID is held: it is at or below the
// thread's hold limit.
func (r *Repository) FeedThreadHeld(ctx context.Context, threadKey string, eventID int64) (bool, error) {
	var until int64
	err := r.rd.QueryRowContext(ctx, `SELECT until_event_id FROM feed_holds WHERE thread_key = ?`, threadKey).Scan(&until)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return eventID <= until, nil
}

// SaveFeedDigest stores a digest event under the next negative id (one below
// the smallest stored, -1 for the first) and returns it with that id.
func (r *Repository) SaveFeedDigest(ctx context.Context, members []int64, e domain.FeedEvent) (domain.FeedEvent, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return e, err
	}
	defer func() { _ = tx.Rollback() }()
	var lowest int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MIN(id), 0) FROM feed_digests`).Scan(&lowest); err != nil {
		return e, err
	}
	e.ID = min(lowest, 0) - 1
	mem, err := feedJSON(nonNilInts(members))
	if err != nil {
		return e, err
	}
	ev, err := feedJSON(e)
	if err != nil {
		return e, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO feed_digests(id, members, event, created_at) VALUES(?,?,?,?)`,
		e.ID, mem, ev, time.Now().UnixMilli()); err != nil {
		return e, err
	}
	if err := tx.Commit(); err != nil {
		return e, err
	}
	r.changed(ChangeOther)
	return e, nil
}

// FeedDigests reads stored digest events by id, ascending id; unknown ids are
// left out.
func (r *Repository) FeedDigests(ctx context.Context, ids []int64) ([]domain.FeedEvent, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	q := `SELECT event FROM feed_digests WHERE id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `) ORDER BY id` //nolint:gosec // G202: the query adds only ? placeholders
	rows, err := r.rd.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.FeedEvent
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var e domain.FeedEvent
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
