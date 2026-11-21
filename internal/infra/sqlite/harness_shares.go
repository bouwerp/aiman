package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// ErrShareNotFound is returned when no harness share has the id.
var ErrShareNotFound = errors.New("harness share not found")

func ensureHarnessShares(db *sql.DB) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS harness_shares (
	id TEXT PRIMARY KEY,
	session TEXT NOT NULL,
	run TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	cap_at DATETIME,
	revoked_at DATETIME
);
CREATE INDEX IF NOT EXISTS harness_shares_session ON harness_shares(session);
CREATE TABLE IF NOT EXISTS gateway_announce (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	base TEXT NOT NULL,
	console_port INTEGER NOT NULL,
	scheme TEXT NOT NULL,
	pid INTEGER NOT NULL,
	at DATETIME NOT NULL
);`
	_, err := db.ExecContext(context.Background(), ddl)
	return err
}

// SaveHarnessShare stores a share, replacing one with the same id.
func (r *Repository) SaveHarnessShare(ctx context.Context, s domain.HarnessShare) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO harness_shares (id, session, run, created_at, cap_at, revoked_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET session = excluded.session, run = excluded.run,
	created_at = excluded.created_at, cap_at = excluded.cap_at, revoked_at = excluded.revoked_at;`,
		s.ID, s.Session, s.Run, s.CreatedAt.UTC(), optTime(s.CapAt), optTime(s.RevokedAt))
	if err != nil {
		return fmt.Errorf("saving harness share: %w", err)
	}
	return nil
}

func optTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}

// GetHarnessShare reads one share; a missing id is ErrShareNotFound.
func (r *Repository) GetHarnessShare(ctx context.Context, id string) (domain.HarnessShare, error) {
	var s domain.HarnessShare
	var capAt, revoked sql.NullTime
	err := r.rd.QueryRowContext(ctx, `SELECT id, session, run, created_at, cap_at, revoked_at
FROM harness_shares WHERE id = ?;`, id).Scan(&s.ID, &s.Session, &s.Run, &s.CreatedAt, &capAt, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.HarnessShare{}, ErrShareNotFound
	}
	if err != nil {
		return domain.HarnessShare{}, fmt.Errorf("reading harness share: %w", err)
	}
	if capAt.Valid {
		t := capAt.Time
		s.CapAt = &t
	}
	if revoked.Valid {
		t := revoked.Time
		s.RevokedAt = &t
	}
	return s, nil
}

// RevokeHarnessShares revokes the session's shares not yet revoked and
// returns how many it changed. A revoked share keeps its first revoked_at.
func (r *Repository) RevokeHarnessShares(ctx context.Context, session string, at time.Time) (int, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE harness_shares SET revoked_at = ?
WHERE session = ? AND revoked_at IS NULL;`, at.UTC(), session)
	if err != nil {
		return 0, fmt.Errorf("revoking harness shares: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// SaveGatewayAnnounce stores the gateway's announce; there is one row.
func (r *Repository) SaveGatewayAnnounce(ctx context.Context, a domain.GatewayAnnounce) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO gateway_announce (id, base, console_port, scheme, pid, at)
VALUES (1, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET base = excluded.base, console_port = excluded.console_port,
	scheme = excluded.scheme, pid = excluded.pid, at = excluded.at;`,
		a.Base, a.ConsolePort, a.Scheme, a.PID, a.At.UTC())
	if err != nil {
		return fmt.Errorf("saving gateway announce: %w", err)
	}
	return nil
}

// GetGatewayAnnounce reads the gateway's last announce; ok is false when it
// never announced.
func (r *Repository) GetGatewayAnnounce(ctx context.Context) (domain.GatewayAnnounce, bool, error) {
	var a domain.GatewayAnnounce
	err := r.rd.QueryRowContext(ctx, `SELECT base, console_port, scheme, pid, at FROM gateway_announce WHERE id = 1;`).
		Scan(&a.Base, &a.ConsolePort, &a.Scheme, &a.PID, &a.At)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.GatewayAnnounce{}, false, nil
	}
	if err != nil {
		return domain.GatewayAnnounce{}, false, fmt.Errorf("reading gateway announce: %w", err)
	}
	return a, true, nil
}

// ListLiveHarnessShares returns the session's shares not yet revoked.
func (r *Repository) ListLiveHarnessShares(ctx context.Context, session string) ([]domain.HarnessShare, error) {
	rows, err := r.rd.QueryContext(ctx, `SELECT id, session, run, created_at FROM harness_shares
WHERE session = ? AND revoked_at IS NULL;`, session)
	if err != nil {
		return nil, fmt.Errorf("listing harness shares: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.HarnessShare
	for rows.Next() {
		var s domain.HarnessShare
		if err := rows.Scan(&s.ID, &s.Session, &s.Run, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
