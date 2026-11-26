package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

const defaultSessionEventLimit = 1000

// ensureSessionEvents creates the append-only ledger. It is separate from the
// sessions table: events are never read on the request path and have no
// COALESCE rules.
func ensureSessionEvents(db *sql.DB) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS session_events (
	session_id TEXT NOT NULL,
	seq INTEGER NOT NULL,
	at DATETIME NOT NULL,
	actor TEXT NOT NULL,
	kind TEXT NOT NULL,
	payload TEXT NOT NULL DEFAULT '{}',
	hash TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (session_id, seq)
);
CREATE INDEX IF NOT EXISTS session_events_at ON session_events (at);`
	if _, err := db.ExecContext(context.Background(), ddl); err != nil {
		return err
	}
	return nil
}

// AppendSessionEvent writes one event and returns its per-session sequence.
// The sequence is allocated by the INSERT itself, so concurrent appends cannot
// reuse a number. Hash is computed when the caller leaves it empty.
func (r *Repository) AppendSessionEvent(ctx context.Context, ev domain.SessionEvent) (int64, error) {
	if ev.SessionID == "" {
		return 0, fmt.Errorf("session event has no session id")
	}
	if ev.Kind == "" {
		return 0, fmt.Errorf("session event has no kind")
	}
	if ev.Actor == "" {
		ev.Actor = domain.EventActorUnknown
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	payload := ev.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	if !json.Valid(payload) {
		return 0, fmt.Errorf("session event payload is not JSON")
	}
	hash := ev.Hash
	if hash == "" {
		hash = payloadHash(payload)
	}
	var seq int64
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO session_events (session_id, seq, at, actor, kind, payload, hash)
		SELECT ?, COALESCE(MAX(seq), 0) + 1, ?, ?, ?, ?, ?
		FROM session_events WHERE session_id = ?
		RETURNING seq`,
		ev.SessionID, ev.At.UTC(), string(ev.Actor), string(ev.Kind), string(payload), hash, ev.SessionID,
	).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("appending session event: %w", err)
	}
	r.changed(ChangeOther)
	return seq, nil
}

// ListSessionEvents returns a session's events after since, oldest first. A
// limit <= 0 uses defaultSessionEventLimit.
func (r *Repository) ListSessionEvents(ctx context.Context, sessionID string, since int64, limit int) ([]domain.SessionEvent, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	if limit <= 0 {
		limit = defaultSessionEventLimit
	}
	return r.querySessionEvents(ctx, `
		SELECT session_id, seq, at, actor, kind, payload, hash
		FROM session_events
		WHERE session_id = ? AND seq > ?
		ORDER BY seq ASC
		LIMIT ?`, sessionID, since, limit)
}

// ListRecentSessionEvents returns a session's newest limit events, oldest of
// them first. A limit <= 0 uses defaultSessionEventLimit.
func (r *Repository) ListRecentSessionEvents(ctx context.Context, sessionID string, limit int) ([]domain.SessionEvent, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	if limit <= 0 {
		limit = defaultSessionEventLimit
	}
	out, err := r.querySessionEvents(ctx, `
		SELECT session_id, seq, at, actor, kind, payload, hash
		FROM session_events
		WHERE session_id = ?
		ORDER BY seq DESC
		LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	slices.Reverse(out)
	return out, nil
}

func (r *Repository) querySessionEvents(ctx context.Context, query string, args ...any) ([]domain.SessionEvent, error) {
	rows, err := r.rd.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing session events: %w", err)
	}
	defer rows.Close()
	var out []domain.SessionEvent
	for rows.Next() {
		var ev domain.SessionEvent
		var at time.Time
		var actor, kind, payload, hash string
		if err := rows.Scan(&ev.SessionID, &ev.Seq, &at, &actor, &kind, &payload, &hash); err != nil {
			return nil, fmt.Errorf("scanning session event: %w", err)
		}
		ev.At = at.UTC()
		ev.Actor = domain.EventActor(actor)
		ev.Kind = domain.EventKind(kind)
		ev.Payload = json.RawMessage(payload)
		ev.Hash = hash
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing session events: %w", err)
	}
	return out, nil
}

func payloadHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// LedgerRow is a ledger event with the rowid that orders the whole table.
// Per-session seq cannot order events across sessions; the rowid can.
type LedgerRow struct {
	RowID int64
	domain.SessionEvent
}

// ListEventsAfterRowID returns events with rowid above afterRowID across all
// sessions, oldest first, optionally limited to kinds. A limit <= 0 uses
// defaultSessionEventLimit.
func (r *Repository) ListEventsAfterRowID(ctx context.Context, afterRowID int64, kinds []domain.EventKind, limit int) ([]LedgerRow, error) {
	if limit <= 0 {
		limit = defaultSessionEventLimit
	}
	query := `SELECT rowid, session_id, seq, at, actor, kind, payload, hash
		FROM session_events WHERE rowid > ?`
	args := []any{afterRowID}
	if len(kinds) > 0 {
		marks := make([]string, len(kinds))
		for i, k := range kinds {
			marks[i] = "?"
			args = append(args, string(k))
		}
		query += " AND kind IN (" + strings.Join(marks, ",") + ")"
	}
	query += " ORDER BY rowid ASC LIMIT ?"
	args = append(args, limit)
	rows, err := r.rd.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing ledger rows: %w", err)
	}
	defer rows.Close()
	var out []LedgerRow
	for rows.Next() {
		var row LedgerRow
		var at time.Time
		var actor, kind, payload, hash string
		if err := rows.Scan(&row.RowID, &row.SessionID, &row.Seq, &at, &actor, &kind, &payload, &hash); err != nil {
			return nil, fmt.Errorf("scanning ledger row: %w", err)
		}
		row.At = at.UTC()
		row.Actor = domain.EventActor(actor)
		row.Kind = domain.EventKind(kind)
		row.Payload = json.RawMessage(payload)
		row.Hash = hash
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing ledger rows: %w", err)
	}
	return out, nil
}

// LastEventRowID is the newest ledger rowid, 0 when the ledger is empty.
func (r *Repository) LastEventRowID(ctx context.Context) (int64, error) {
	var last int64
	if err := r.rd.QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid), 0) FROM session_events`).Scan(&last); err != nil {
		return 0, fmt.Errorf("reading last ledger rowid: %w", err)
	}
	return last, nil
}
