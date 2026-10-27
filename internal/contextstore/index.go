package contextstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/bouwerp/aiman/internal/domain"
)

// Index keeps a searchable copy of the context notes in serve's SQLite file.
type Index struct{ db *sql.DB }

// OpenIndex creates the index tables when they are absent. It fails when the
// SQLite build has no FTS5 support.
func OpenIndex(db *sql.DB) (*Index, error) {
	stmts := []string{
		`CREATE VIRTUAL TABLE IF NOT EXISTS context_fts USING fts5(id UNINDEXED, title, abstract, body, tokenize='unicode61 remove_diacritics 2');`,
		`CREATE TABLE IF NOT EXISTS context_index(
			id TEXT PRIMARY KEY,
			path TEXT NOT NULL,
			ns TEXT,
			key TEXT,
			type TEXT,
			supersedes TEXT,
			valid_until INTEGER,
			created INTEGER,
			title TEXT,
			abstract TEXT,
			session TEXT,
			mtime INTEGER NOT NULL DEFAULT 0
		);`,
		`CREATE TABLE IF NOT EXISTS context_index_meta(k TEXT PRIMARY KEY, v TEXT);`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return nil, fmt.Errorf("opening context index: %w", err)
		}
	}
	return &Index{db: db}, nil
}

// Upsert replaces one note in both the metadata and FTS tables.
func (ix *Index) Upsert(ctx context.Context, e domain.ContextEntry, path string, mtime time.Time) error {
	validUntil := int64(0)
	if !e.ValidUntil.IsZero() {
		validUntil = e.ValidUntil.Unix()
	}
	created := int64(0)
	if !e.CreatedAt.IsZero() {
		created = e.CreatedAt.Unix()
	}
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM context_fts WHERE id = ?`, e.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO context_fts(id, title, abstract, body) VALUES(?,?,?,?)`,
		e.ID, e.Title, e.Abstract, e.Body); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO context_index(id, path, ns, key, type, supersedes, valid_until, created, title, abstract, session, mtime)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET
		   path=excluded.path, ns=excluded.ns, key=excluded.key, type=excluded.type,
		   supersedes=excluded.supersedes, valid_until=excluded.valid_until,
		   created=excluded.created, title=excluded.title, abstract=excluded.abstract,
		   session=excluded.session, mtime=excluded.mtime`,
		e.ID, path, e.Namespace, e.Key, string(e.Type), e.Supersedes, validUntil, created,
		e.Title, e.Abstract, e.SessionID, mtime.UnixNano()); err != nil {
		return err
	}
	return tx.Commit()
}

// Remove deletes one note from both tables.
func (ix *Index) Remove(ctx context.Context, id string) error {
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM context_fts WHERE id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM context_index WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// Paths returns the indexed note id to file path mapping.
func (ix *Index) Paths(ctx context.Context) (map[string]string, error) {
	rows, err := ix.db.QueryContext(ctx, `SELECT id, path FROM context_index`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]string)
	for rows.Next() {
		var id, path string
		if err := rows.Scan(&id, &path); err != nil {
			return nil, err
		}
		out[id] = path
	}
	return out, rows.Err()
}

// PathOf returns one indexed note's file path.
func (ix *Index) PathOf(ctx context.Context, id string) (string, error) {
	var path string
	err := ix.db.QueryRowContext(ctx, `SELECT path FROM context_index WHERE id = ?`, id).Scan(&path)
	return path, err
}

// Search returns ranked notes without bodies. The FTS query is built from
// words only, so user text cannot inject FTS syntax.
func (ix *Index) Search(ctx context.Context, q domain.ContextQuery, live bool, now time.Time) ([]domain.ContextEntry, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	var where []string
	var args []any
	if q.Namespace != "" {
		where = append(where, "mi.ns = ?")
		args = append(args, q.Namespace)
	}
	if q.Key != "" {
		where = append(where, "(mi.key = ? OR mi.key = ?)")
		args = append(args, q.Key, SafeKey(q.Key))
	}
	if text := ftsQuery(q.Text); text != "" {
		where = append(where, "mi.id IN (SELECT id FROM context_fts WHERE context_fts MATCH ?)")
		args = append(args, text)
	} else if strings.TrimSpace(q.Text) != "" {
		return nil, nil
	}
	if live {
		where = append(where, "(mi.valid_until = 0 OR mi.valid_until > ?)")
		args = append(args, now.Unix())
		where = append(where, "mi.id NOT IN (SELECT supersedes FROM context_index WHERE supersedes <> '')")
	}
	var query strings.Builder
	query.WriteString(`SELECT mi.id, mi.ns, mi.key, mi.title, mi.abstract, mi.type, mi.supersedes,
		mi.valid_until, mi.created, mi.session FROM context_index mi`)
	for i, clause := range where {
		if i == 0 {
			query.WriteString(" WHERE ")
		} else {
			query.WriteString(" AND ")
		}
		query.WriteString(clause)
	}
	query.WriteString(` ORDER BY CASE WHEN mi.type IN ('decision','pitfall','convention') THEN 0 ELSE 1 END,
		mi.created DESC, mi.id DESC LIMIT ?`)
	args = append(args, limit)
	rows, err := ix.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.ContextEntry
	for rows.Next() {
		var e domain.ContextEntry
		var typ string
		var supersedes, session sql.NullString
		var validUntil, created int64
		if err := rows.Scan(&e.ID, &e.Namespace, &e.Key, &e.Title, &e.Abstract, &typ,
			&supersedes, &validUntil, &created, &session); err != nil {
			return nil, err
		}
		e.Type = domain.ContextType(typ)
		if e.Type == "" {
			e.Type = domain.ContextTypeFact
		}
		e.Supersedes = supersedes.String
		e.SessionID = session.String
		if validUntil > 0 {
			e.ValidUntil = time.Unix(validUntil, 0).UTC()
		}
		if created > 0 {
			e.CreatedAt = time.Unix(created, 0).UTC()
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// HighWater returns the newest file mtime the index has seen.
func (ix *Index) HighWater(ctx context.Context) (time.Time, error) {
	var raw string
	err := ix.db.QueryRowContext(ctx, `SELECT v FROM context_index_meta WHERE k = 'high_water'`).Scan(&raw)
	if err == sql.ErrNoRows {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing context high water: %w", err)
	}
	return at, nil
}

// SetHighWater stores the newest file mtime the index has seen.
func (ix *Index) SetHighWater(ctx context.Context, at time.Time) error {
	_, err := ix.db.ExecContext(ctx,
		`INSERT INTO context_index_meta(k, v) VALUES('high_water', ?)
		 ON CONFLICT(k) DO UPDATE SET v = excluded.v`, at.UTC().Format(time.RFC3339Nano))
	return err
}

// ftsQuery turns free text into quoted prefix tokens joined by AND. It returns
// an empty string when the text has no letters or digits.
func ftsQuery(text string) string {
	var tokens []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			tokens = append(tokens, `"`+b.String()+`"*`)
			b.Reset()
		}
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return strings.Join(tokens, " ")
}
