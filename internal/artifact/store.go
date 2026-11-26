package artifact

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Record is one artifact as serve's database keeps it.
type Record struct {
	ID      string
	Title   string
	Session string // owning session id
	Latest  int
	Share   ShareList
	Created time.Time
	Deleted bool
}

// Version is one published version of an artifact.
type Version struct {
	N       int
	Session string
	Created time.Time
	Files   []FileInfo
}

// FileInfo describes one file of a version.
type FileInfo struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Type   string `json:"type"`
}

// SQLStore keeps records and versions in serve's database (tables
// artifacts and artifact_versions, created by OpenSQLStore).
type SQLStore struct {
	db *sql.DB
}

const storeSchema = `
CREATE TABLE IF NOT EXISTS artifacts (
	id         TEXT PRIMARY KEY,
	title      TEXT NOT NULL DEFAULT '',
	session_id TEXT NOT NULL DEFAULT '',
	latest     INTEGER NOT NULL DEFAULT 0,
	share_json TEXT NOT NULL DEFAULT '{}',
	created_at INTEGER NOT NULL,
	deleted    INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS artifact_versions (
	artifact_id TEXT NOT NULL,
	n           INTEGER NOT NULL,
	session_id  TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL,
	files_json  TEXT NOT NULL DEFAULT '[]',
	PRIMARY KEY (artifact_id, n)
);`

// OpenSQLStore creates the tables when missing.
func OpenSQLStore(db *sql.DB) (*SQLStore, error) {
	if _, err := db.Exec(storeSchema); err != nil {
		return nil, fmt.Errorf("artifact store schema: %w", err)
	}
	return &SQLStore{db: db}, nil
}

type rowScanner interface{ Scan(...any) error }

func scanRecord(row rowScanner) (Record, error) {
	var (
		r       Record
		share   string
		created int64
		deleted int
	)
	if err := row.Scan(&r.ID, &r.Title, &r.Session, &r.Latest, &share, &created, &deleted); err != nil {
		return Record{}, err
	}
	if err := json.Unmarshal([]byte(share), &r.Share); err != nil {
		return Record{}, fmt.Errorf("artifact %s share: %w", r.ID, err)
	}
	r.Share = r.Share.normal()
	r.Created = time.UnixMilli(created).UTC()
	r.Deleted = deleted != 0
	return r, nil
}

const recordCols = `id, title, session_id, latest, share_json, created_at, deleted`

// Get reads one record, deleted or not.
func (s *SQLStore) Get(ctx context.Context, id string) (Record, error) {
	r, err := scanRecord(s.db.QueryRowContext(ctx, `SELECT `+recordCols+` FROM artifacts WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return r, err
}

// Save inserts or replaces a record.
func (s *SQLStore) Save(ctx context.Context, r Record) error {
	share, err := json.Marshal(r.Share.normal())
	if err != nil {
		return err
	}
	deleted := 0
	if r.Deleted {
		deleted = 1
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO artifacts (`+recordCols+`) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET title = excluded.title, session_id = excluded.session_id,
		latest = excluded.latest, share_json = excluded.share_json, deleted = excluded.deleted`,
		r.ID, r.Title, r.Session, r.Latest, string(share), r.Created.UnixMilli(), deleted)
	return err
}

// AddVersion records version v of artifact id.
func (s *SQLStore) AddVersion(ctx context.Context, id string, v Version) error {
	files, err := json.Marshal(v.Files)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO artifact_versions (artifact_id, n, session_id, created_at, files_json)
		VALUES (?, ?, ?, ?, ?)`, id, v.N, v.Session, v.Created.UnixMilli(), string(files))
	return err
}

// Versions lists an artifact's versions, oldest first.
func (s *SQLStore) Versions(ctx context.Context, id string) ([]Version, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT n, session_id, created_at, files_json FROM artifact_versions
		WHERE artifact_id = ? ORDER BY n`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		var (
			v       Version
			created int64
			files   string
		)
		if err := rows.Scan(&v.N, &v.Session, &created, &files); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(files), &v.Files); err != nil {
			return nil, fmt.Errorf("artifact %s v%d files: %w", id, v.N, err)
		}
		v.Created = time.UnixMilli(created).UTC()
		out = append(out, v)
	}
	return out, rows.Err()
}

// List returns every record not deleted, newest first.
func (s *SQLStore) List(ctx context.Context) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+recordCols+` FROM artifacts WHERE deleted = 0
		ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
