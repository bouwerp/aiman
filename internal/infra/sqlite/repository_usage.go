package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// usageHourLayout is a UTC hour truncated to the clock hour. It sorts as text
// in the same order as the instants it names.
const usageHourLayout = "2006-01-02T15"

func ensureUsageTables(db *sql.DB) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS usage_cursors (
	session_id TEXT NOT NULL,
	path TEXT NOT NULL,
	byte_offset INTEGER NOT NULL DEFAULT 0,
	fingerprint TEXT NOT NULL DEFAULT '',
	size INTEGER NOT NULL DEFAULT 0,
	mod_time DATETIME,
	state_json TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (session_id, path)
);
CREATE TABLE IF NOT EXISTS usage_seen (
	session_id TEXT NOT NULL,
	record_id TEXT NOT NULL,
	PRIMARY KEY (session_id, record_id)
);
CREATE TABLE IF NOT EXISTS usage_buckets (
	session_id TEXT NOT NULL,
	hour TEXT NOT NULL,
	model TEXT NOT NULL,
	input INTEGER NOT NULL DEFAULT 0,
	output INTEGER NOT NULL DEFAULT 0,
	cache_read INTEGER NOT NULL DEFAULT 0,
	cache_write_5m INTEGER NOT NULL DEFAULT 0,
	cache_write_1h INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (session_id, hour, model)
);
CREATE TABLE IF NOT EXISTS usage_sessions (
	session_id TEXT PRIMARY KEY,
	name TEXT NOT NULL DEFAULT '',
	group_name TEXT NOT NULL DEFAULT '',
	issue_key TEXT NOT NULL DEFAULT '',
	agent_name TEXT NOT NULL DEFAULT '',
	role TEXT NOT NULL DEFAULT '',
	parent_id TEXT NOT NULL DEFAULT '',
	last_input_tokens INTEGER NOT NULL DEFAULT 0,
	context_window INTEGER NOT NULL DEFAULT 0,
	context_source TEXT NOT NULL DEFAULT '',
	health_known INTEGER NOT NULL DEFAULT 0,
	health_tool_calls INTEGER NOT NULL DEFAULT 0,
	health_read_calls INTEGER NOT NULL DEFAULT 0,
	health_edit_calls INTEGER NOT NULL DEFAULT 0,
	health_edits_without_read INTEGER NOT NULL DEFAULT 0,
	health_first_test_at DATETIME,
	health_retries INTEGER NOT NULL DEFAULT 0,
	health_reads TEXT NOT NULL DEFAULT '',
	budget_state TEXT NOT NULL DEFAULT '',
	updated_at DATETIME
);`
	if _, err := db.ExecContext(context.Background(), ddl); err != nil {
		return err
	}
	return ensureUsageContextColumns(db)
}

// ensureUsageContextColumns adds occupancy and health columns to a
// usage_sessions table created before those columns existed.
// CREATE TABLE IF NOT EXISTS does not alter an existing table.
func ensureUsageContextColumns(db *sql.DB) error {
	for _, stmt := range []string{
		`ALTER TABLE usage_sessions ADD COLUMN last_input_tokens INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE usage_sessions ADD COLUMN context_window INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE usage_sessions ADD COLUMN context_source TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE usage_sessions ADD COLUMN health_known INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE usage_sessions ADD COLUMN health_tool_calls INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE usage_sessions ADD COLUMN health_read_calls INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE usage_sessions ADD COLUMN health_edit_calls INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE usage_sessions ADD COLUMN health_edits_without_read INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE usage_sessions ADD COLUMN health_first_test_at DATETIME`,
		`ALTER TABLE usage_sessions ADD COLUMN health_retries INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE usage_sessions ADD COLUMN health_reads TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE usage_sessions ADD COLUMN budget_state TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE usage_sessions ADD COLUMN role TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE usage_sessions ADD COLUMN parent_id TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("ensuring usage context columns: %w", err)
		}
	}
	return nil
}

// UsageCursors returns the stored parser positions for one session.
func (r *Repository) UsageCursors(ctx context.Context, sessionID string) ([]domain.UsageCursor, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT session_id, path, byte_offset, fingerprint, size, mod_time, state_json
		FROM usage_cursors WHERE session_id = ? ORDER BY path`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("listing usage cursors: %w", err)
	}
	defer rows.Close()
	var out []domain.UsageCursor
	for rows.Next() {
		cur, err := scanUsageCursor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, cur)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing usage cursors: %w", err)
	}
	return out, nil
}

// ApplyUsage folds records into hourly buckets and replaces the cursor, in one
// transaction. A non-empty record ID is claimed in usage_seen; a row that
// already existed contributes no tokens. An empty ID is not claimed and its
// tokens are always added. The cursor is written even when nothing was added.
// state_json is stored as the given string.
func (r *Repository) ApplyUsage(ctx context.Context, cur domain.UsageCursor, recs []domain.UsageRecord) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("applying usage: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	added := false
	for _, rec := range recs {
		keep, err := claimUsageRecord(ctx, tx, cur.SessionID, rec.ID)
		if err != nil {
			return false, err
		}
		if !keep {
			continue
		}
		if err := addUsageBucket(ctx, tx, cur.SessionID, rec); err != nil {
			return false, err
		}
		added = true
	}
	if err := upsertUsageCursor(ctx, tx, cur); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("applying usage: %w", err)
	}
	return added, nil
}

// claimUsageRecord reports whether rec's tokens should be counted.
// An empty id is never deduplicated.
func claimUsageRecord(ctx context.Context, tx *sql.Tx, sessionID, id string) (bool, error) {
	if id == "" {
		return true, nil
	}
	res, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO usage_seen (session_id, record_id) VALUES (?, ?)`,
		sessionID, id)
	if err != nil {
		return false, fmt.Errorf("recording seen usage id: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("recording seen usage id: %w", err)
	}
	return n > 0, nil
}

func addUsageBucket(ctx context.Context, tx *sql.Tx, sessionID string, rec domain.UsageRecord) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO usage_buckets (
			session_id, hour, model, input, output, cache_read, cache_write_5m, cache_write_1h
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, hour, model) DO UPDATE SET
			input = input + excluded.input,
			output = output + excluded.output,
			cache_read = cache_read + excluded.cache_read,
			cache_write_5m = cache_write_5m + excluded.cache_write_5m,
			cache_write_1h = cache_write_1h + excluded.cache_write_1h`,
		sessionID, usageHourKey(rec.At), rec.Model,
		rec.Tokens.Input, rec.Tokens.Output, rec.Tokens.CacheRead, rec.Tokens.CacheWrite5m, rec.Tokens.CacheWrite1h)
	if err != nil {
		return fmt.Errorf("updating usage bucket: %w", err)
	}
	return nil
}

func upsertUsageCursor(ctx context.Context, tx *sql.Tx, cur domain.UsageCursor) error {
	var mod any
	if !cur.ModTime.IsZero() {
		mod = cur.ModTime.UTC()
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO usage_cursors (session_id, path, byte_offset, fingerprint, size, mod_time, state_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, path) DO UPDATE SET
			byte_offset = excluded.byte_offset,
			fingerprint = excluded.fingerprint,
			size = excluded.size,
			mod_time = excluded.mod_time,
			state_json = excluded.state_json`,
		cur.SessionID, cur.Path, cur.Offset, cur.Fingerprint, cur.Size, mod, cur.State)
	if err != nil {
		return fmt.Errorf("saving usage cursor: %w", err)
	}
	return nil
}

// UpsertUsageSession copies session labels onto the usage row. An empty name,
// group, issue key, or agent name leaves the stored value in place.
// Occupancy columns change only when HasContext is set, so a label refresh
// does not wipe the last turn. A HasContext write replaces them, including
// a later turn whose token count is smaller.
func (r *Repository) UpsertUsageSession(ctx context.Context, m domain.UsageSessionMeta) error {
	has := 0
	if m.HasContext {
		has = 1
	}
	hasHealth := 0
	if m.HasHealth {
		hasHealth = 1
	}
	known := 0
	if m.HealthKnown {
		known = 1
	}
	var firstTest any
	if !m.HealthFirstTestAt.IsZero() {
		firstTest = m.HealthFirstTestAt.UTC()
	}
	hasBudget := 0
	if m.HasBudgetState {
		hasBudget = 1
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO usage_sessions (
			session_id, name, group_name, issue_key, agent_name,
			last_input_tokens, context_window, context_source,
			health_known, health_tool_calls, health_read_calls, health_edit_calls,
			health_edits_without_read, health_first_test_at, health_retries, health_reads,
			budget_state, role, parent_id,
			updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			name = COALESCE(NULLIF(excluded.name, ''), usage_sessions.name),
			group_name = COALESCE(NULLIF(excluded.group_name, ''), usage_sessions.group_name),
			issue_key = COALESCE(NULLIF(excluded.issue_key, ''), usage_sessions.issue_key),
			agent_name = COALESCE(NULLIF(excluded.agent_name, ''), usage_sessions.agent_name),
			role = COALESCE(NULLIF(excluded.role, ''), usage_sessions.role),
			parent_id = COALESCE(NULLIF(excluded.parent_id, ''), usage_sessions.parent_id),
			last_input_tokens = CASE WHEN ? = 1 THEN excluded.last_input_tokens ELSE usage_sessions.last_input_tokens END,
			context_window = CASE WHEN ? = 1 THEN excluded.context_window ELSE usage_sessions.context_window END,
			context_source = CASE WHEN ? = 1 THEN excluded.context_source ELSE usage_sessions.context_source END,
			health_known = CASE WHEN ? = 1 THEN excluded.health_known ELSE usage_sessions.health_known END,
			health_tool_calls = CASE WHEN ? = 1 THEN excluded.health_tool_calls ELSE usage_sessions.health_tool_calls END,
			health_read_calls = CASE WHEN ? = 1 THEN excluded.health_read_calls ELSE usage_sessions.health_read_calls END,
			health_edit_calls = CASE WHEN ? = 1 THEN excluded.health_edit_calls ELSE usage_sessions.health_edit_calls END,
			health_edits_without_read = CASE WHEN ? = 1 THEN excluded.health_edits_without_read ELSE usage_sessions.health_edits_without_read END,
			health_first_test_at = CASE WHEN ? = 1 THEN excluded.health_first_test_at ELSE usage_sessions.health_first_test_at END,
			health_retries = CASE WHEN ? = 1 THEN excluded.health_retries ELSE usage_sessions.health_retries END,
			health_reads = CASE WHEN ? = 1 THEN excluded.health_reads ELSE usage_sessions.health_reads END,
			budget_state = CASE WHEN ? = 1 THEN excluded.budget_state ELSE usage_sessions.budget_state END,
			updated_at = excluded.updated_at`,
		m.SessionID, m.Name, m.Group, m.IssueKey, m.AgentName,
		m.LastInputTokens, m.ContextWindow, m.ContextSource,
		known, m.HealthToolCalls, m.HealthReadCalls, m.HealthEditCalls,
		m.HealthEditsWithoutRead, firstTest, m.HealthRetries, m.HealthReads,
		m.BudgetState, m.Role, m.ParentID,
		time.Now().UTC(),
		has, has, has,
		hasHealth, hasHealth, hasHealth, hasHealth, hasHealth, hasHealth, hasHealth, hasHealth,
		hasBudget)
	if err != nil {
		return fmt.Errorf("saving usage session: %w", err)
	}
	return nil
}

// UsageSessions returns every stored usage label, including sessions that
// have been deleted from the sessions table.
func (r *Repository) UsageSessions(ctx context.Context) ([]domain.UsageSessionMeta, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT session_id, name, group_name, issue_key, agent_name,
			last_input_tokens, context_window, context_source,
			health_known, health_tool_calls, health_read_calls, health_edit_calls,
			health_edits_without_read, health_first_test_at, health_retries, health_reads,
			budget_state, role, parent_id
		FROM usage_sessions ORDER BY session_id`)
	if err != nil {
		return nil, fmt.Errorf("listing usage sessions: %w", err)
	}
	defer rows.Close()
	var out []domain.UsageSessionMeta
	for rows.Next() {
		var m domain.UsageSessionMeta
		var known int
		var firstTest sql.NullTime
		if err := rows.Scan(
			&m.SessionID, &m.Name, &m.Group, &m.IssueKey, &m.AgentName,
			&m.LastInputTokens, &m.ContextWindow, &m.ContextSource,
			&known, &m.HealthToolCalls, &m.HealthReadCalls, &m.HealthEditCalls,
			&m.HealthEditsWithoutRead, &firstTest, &m.HealthRetries, &m.HealthReads,
			&m.BudgetState, &m.Role, &m.ParentID,
		); err != nil {
			return nil, fmt.Errorf("listing usage sessions: %w", err)
		}
		m.HealthKnown = known != 0
		if firstTest.Valid {
			m.HealthFirstTestAt = firstTest.Time
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing usage sessions: %w", err)
	}
	return out, nil
}

// UsageBuckets returns hourly totals. A nil sessionIDs slice is every session.
// A non-nil empty slice is no rows. A zero since has no lower bound. A bucket
// is included when its hour is not before since.
func (r *Repository) UsageBuckets(ctx context.Context, sessionIDs []string, since time.Time) ([]domain.UsageBucket, error) {
	if sessionIDs != nil && len(sessionIDs) == 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT session_id, hour, model, input, output, cache_read, cache_write_5m, cache_write_1h
		FROM usage_buckets ORDER BY session_id, hour, model`)
	if err != nil {
		return nil, fmt.Errorf("listing usage buckets: %w", err)
	}
	defer rows.Close()
	allow := usageSessionAllow(sessionIDs)
	var out []domain.UsageBucket
	for rows.Next() {
		b, err := scanUsageBucket(rows)
		if err != nil {
			return nil, err
		}
		if allow != nil {
			if _, ok := allow[b.SessionID]; !ok {
				continue
			}
		}
		if !since.IsZero() && b.Hour.Before(since) {
			continue
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing usage buckets: %w", err)
	}
	return out, nil
}

func usageSessionAllow(sessionIDs []string) map[string]struct{} {
	if sessionIDs == nil {
		return nil
	}
	allow := make(map[string]struct{}, len(sessionIDs))
	for _, id := range sessionIDs {
		allow[id] = struct{}{}
	}
	return allow
}

func usageHourKey(t time.Time) string {
	return t.UTC().Truncate(time.Hour).Format(usageHourLayout)
}

func parseUsageHour(s string) (time.Time, error) {
	t, err := time.ParseInLocation(usageHourLayout, s, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing usage hour %q: %w", s, err)
	}
	return t, nil
}

func scanUsageCursor(rows sessionRowScanner) (domain.UsageCursor, error) {
	var cur domain.UsageCursor
	var mod sql.NullTime
	if err := rows.Scan(&cur.SessionID, &cur.Path, &cur.Offset, &cur.Fingerprint, &cur.Size, &mod, &cur.State); err != nil {
		return domain.UsageCursor{}, fmt.Errorf("scanning usage cursor: %w", err)
	}
	if mod.Valid {
		cur.ModTime = mod.Time
	}
	return cur, nil
}

func scanUsageBucket(rows sessionRowScanner) (domain.UsageBucket, error) {
	var b domain.UsageBucket
	var hour string
	if err := rows.Scan(&b.SessionID, &hour, &b.Model, &b.Tokens.Input, &b.Tokens.Output, &b.Tokens.CacheRead, &b.Tokens.CacheWrite5m, &b.Tokens.CacheWrite1h); err != nil {
		return domain.UsageBucket{}, fmt.Errorf("scanning usage bucket: %w", err)
	}
	parsed, err := parseUsageHour(hour)
	if err != nil {
		return domain.UsageBucket{}, err
	}
	b.Hour = parsed
	return b, nil
}
