package memoryjob

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/google/uuid"
)

// Job is one request to propose memory candidates from a handover.
type Job struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	Reason    string    `json:"reason"`
	Group     string    `json:"group"`
	Repo      string    `json:"repo"`
	Label     string    `json:"label"`
	Source    string    `json:"source"`
	Created   time.Time `json:"created"`
	// Candidates are proposals made by a handover child. A job without
	// them leaves proposing to the laptop.
	Candidates []domain.MemoryCandidate `json:"candidates,omitempty"`
}

// Queue stores memory-candidate jobs in serve's SQLite file.
type Queue struct{ db *sql.DB }

// Open creates the job table when it is absent and adds columns that a
// table created by an older schema lacks.
func Open(db *sql.DB) (*Queue, error) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS memory_jobs(
		id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL,
		reason TEXT,
		group_name TEXT,
		repo_name TEXT,
		label TEXT,
		source TEXT,
		created INTEGER NOT NULL,
		done INTEGER NOT NULL DEFAULT 0,
		candidates TEXT
	);`)
	if err != nil {
		return nil, fmt.Errorf("opening memory jobs: %w", err)
	}
	// CREATE TABLE IF NOT EXISTS does not alter an existing table.
	if _, err := db.Exec(`ALTER TABLE memory_jobs ADD COLUMN candidates TEXT`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return nil, fmt.Errorf("adding memory job candidates: %w", err)
	}
	return &Queue{db: db}, nil
}

// Enqueue replaces any unfinished job for the same session. A job whose id is
// already stored, pending or done, is left as it is.
func (q *Queue) Enqueue(ctx context.Context, j Job) error {
	if j.ID == "" {
		j.ID = uuid.NewString()
	}
	if j.Created.IsZero() {
		j.Created = time.Now().UTC()
	}
	var candidates sql.NullString
	if len(j.Candidates) > 0 {
		b, err := json.Marshal(j.Candidates)
		if err != nil {
			return fmt.Errorf("encoding memory candidates: %w", err)
		}
		candidates = sql.NullString{String: string(b), Valid: true}
	}
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM memory_jobs WHERE id = ?`, j.ID).Scan(&exists); {
	case err == nil:
		return nil // already queued or handed out; a retry must not queue it again
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_jobs WHERE session_id = ? AND done = 0`, j.SessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO memory_jobs(id, session_id, reason, group_name, repo_name, label, source, created, done, candidates)
		 VALUES(?,?,?,?,?,?,?,?,0,?)`,
		j.ID, j.SessionID, j.Reason, j.Group, j.Repo, j.Label, j.Source, j.Created.UnixNano(), candidates); err != nil {
		return err
	}
	return tx.Commit()
}

// Pending returns unfinished jobs oldest first.
func (q *Queue) Pending(ctx context.Context) ([]Job, error) {
	rows, err := q.db.QueryContext(ctx,
		`SELECT id, session_id, reason, group_name, repo_name, label, source, created, candidates
		 FROM memory_jobs WHERE done = 0 ORDER BY created ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Job
	for rows.Next() {
		var j Job
		var created int64
		var candidates sql.NullString
		if err := rows.Scan(&j.ID, &j.SessionID, &j.Reason, &j.Group, &j.Repo, &j.Label, &j.Source, &created, &candidates); err != nil {
			return nil, err
		}
		if candidates.String != "" {
			if err := json.Unmarshal([]byte(candidates.String), &j.Candidates); err != nil {
				return nil, fmt.Errorf("decoding memory candidates for job %s: %w", j.ID, err)
			}
		}
		j.Created = time.Unix(0, created).UTC()
		out = append(out, j)
	}
	return out, rows.Err()
}

// Done marks a job finished. Marking it twice is not an error.
func (q *Queue) Done(ctx context.Context, id string) error {
	_, err := q.db.ExecContext(ctx, `UPDATE memory_jobs SET done = 1 WHERE id = ?`, id)
	return err
}
