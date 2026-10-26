package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	rows, err := r.db.QueryContext(ctx, `
		SELECT session_id, seq, at, actor, kind, payload, hash
		FROM session_events
		WHERE session_id = ? AND seq > ?
		ORDER BY seq ASC
		LIMIT ?`, sessionID, since, limit)
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
