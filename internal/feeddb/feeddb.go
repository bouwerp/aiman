// Package feeddb is the event feed's own database, ~/.aiman/feed.db. The
// collector owns it through Writer; serve reads it through Reader and writes
// only mutes and the prune floor through MuteWriter.
package feeddb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	_ "modernc.org/sqlite"
)

// ErrNoDatabase is returned by OpenReader and OpenMuteWriter when the file
// does not exist yet.
var ErrNoDatabase = errors.New("feed database does not exist")

const (
	fileName    = "feed.db"
	busyTimeout = 5000
	readerConns = 4
)

const schema = `
CREATE TABLE IF NOT EXISTS events(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  source TEXT NOT NULL, external_id TEXT NOT NULL,
  at INTEGER NOT NULL, actor TEXT NOT NULL, actor_name TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL, url TEXT NOT NULL DEFAULT '',
  thread_key TEXT NOT NULL, class TEXT NOT NULL, urgent INTEGER NOT NULL DEFAULT 0,
  own INTEGER NOT NULL DEFAULT 0, enrichment TEXT NOT NULL DEFAULT '', channel TEXT NOT NULL DEFAULT '',
  stored_at INTEGER NOT NULL,
  UNIQUE(source, external_id));
CREATE INDEX IF NOT EXISTS events_thread ON events(thread_key, id);
CREATE TABLE IF NOT EXISTS cursors(source TEXT NOT NULL, key TEXT NOT NULL, value TEXT NOT NULL, PRIMARY KEY(source, key));
CREATE TABLE IF NOT EXISTS health(source TEXT PRIMARY KEY, last_poll INTEGER, last_ok INTEGER, error TEXT NOT NULL DEFAULT '', calls INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS mutes(scope TEXT NOT NULL, key TEXT NOT NULL, until INTEGER NOT NULL, action_id INTEGER NOT NULL, PRIMARY KEY(scope, key));
CREATE TABLE IF NOT EXISTS floor(id INTEGER PRIMARY KEY CHECK(id = 1), min_event_id INTEGER NOT NULL);
`

// Path is the feed database inside an aiman directory.
func Path(aimanDir string) string { return filepath.Join(aimanDir, fileName) }

// CursorKey names one collector cursor.
type CursorKey struct {
	Source domain.FeedSource
	Key    string
}

// Health is one source's last poll outcome.
type Health struct {
	Source   domain.FeedSource `json:"source"`
	LastPoll time.Time         `json:"last_poll"`
	LastOK   time.Time         `json:"last_ok"`
	Error    string            `json:"error,omitempty"`
	Calls    int               `json:"calls"`
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

func pragmas() string {
	return fmt.Sprintf("_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", busyTimeout)
}

// dsn builds a file: URI so a path holding '?' or '#' stays one path.
func dsn(path, extra string) string {
	u := url.URL{Scheme: "file", Path: path}
	s := u.String() + "?" + pragmas()
	if extra != "" {
		s += "&" + extra
	}
	return s
}

func requireFile(path string) error {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNoDatabase
		}
		return err
	}
	return nil
}

// Writer is the collector's handle; it owns the schema.
type Writer struct{ db *sql.DB }

// OpenWriter opens (creating if needed) the database and migrates it.
func OpenWriter(path string) (*Writer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn(path, "_txlock=immediate"))
	if err != nil {
		return nil, fmt.Errorf("open feed database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate feed database: %w", err)
	}
	return &Writer{db: db}, nil
}

// Close closes the handle.
func (w *Writer) Close() error { return w.db.Close() }

// Append inserts the events that are new by (source, external_id), sets the
// cursors and deletes the drop cursors, in one transaction. It returns the
// events stored, with ids. An event with an empty ExternalID fails the call and
// writes nothing.
func (w *Writer) Append(ctx context.Context, events []domain.FeedEvent, cursors map[CursorKey]string, drop ...CursorKey) ([]domain.FeedEvent, error) {
	for _, ev := range events {
		if ev.ExternalID == "" {
			return nil, errors.New("feed event has an empty external id")
		}
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UnixMilli()
	stored := make([]domain.FeedEvent, 0, len(events))
	for _, ev := range events {
		res, err := tx.ExecContext(ctx, `INSERT INTO events(source, external_id, at, actor, actor_name, kind, title, body, url, thread_key, class, urgent, own, enrichment, channel, stored_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(source, external_id) DO NOTHING`,
			string(ev.Source), ev.ExternalID, ms(ev.At), ev.Actor, ev.ActorName, ev.Kind, ev.Title, ev.Body, ev.URL,
			ev.ThreadKey, string(ev.Class), boolInt(ev.Urgent), boolInt(ev.Own), ev.Enrichment, ev.Channel, now)
		if err != nil {
			return nil, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, err
		}
		if n == 0 {
			continue
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		ev.ID = id
		stored = append(stored, ev)
	}
	for k, v := range cursors {
		if _, err := tx.ExecContext(ctx, `INSERT INTO cursors(source, key, value) VALUES(?,?,?)
			ON CONFLICT(source, key) DO UPDATE SET value = excluded.value`, string(k.Source), k.Key, v); err != nil {
			return nil, err
		}
	}
	if err := deleteCursors(ctx, tx, drop); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return stored, nil
}

// DeleteCursors removes one source's cursors by key, in one transaction. A key
// that is not stored is no error.
func (w *Writer) DeleteCursors(ctx context.Context, source domain.FeedSource, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	drop := make([]CursorKey, 0, len(keys))
	for _, k := range keys {
		drop = append(drop, CursorKey{Source: source, Key: k})
	}
	if err := deleteCursors(ctx, tx, drop); err != nil {
		return err
	}
	return tx.Commit()
}

func deleteCursors(ctx context.Context, tx *sql.Tx, keys []CursorKey) error {
	for _, k := range keys {
		if _, err := tx.ExecContext(ctx, `DELETE FROM cursors WHERE source = ? AND key = ?`, string(k.Source), k.Key); err != nil {
			return err
		}
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Cursors returns one source's cursors by key.
func (w *Writer) Cursors(ctx context.Context, source domain.FeedSource) (map[string]string, error) {
	rows, err := w.db.QueryContext(ctx, `SELECT key, value FROM cursors WHERE source = ?`, string(source))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SetHealth records a source's last poll outcome.
func (w *Writer) SetHealth(ctx context.Context, h Health) error {
	_, err := w.db.ExecContext(ctx, `INSERT INTO health(source, last_poll, last_ok, error, calls) VALUES(?,?,?,?,?)
		ON CONFLICT(source) DO UPDATE SET last_poll = excluded.last_poll, last_ok = excluded.last_ok, error = excluded.error, calls = excluded.calls`,
		string(h.Source), ms(h.LastPoll), ms(h.LastOK), h.Error, h.Calls)
	return err
}

// Prune deletes events older than before and strictly below the prune floor.
// With no floor row it deletes nothing.
func (w *Writer) Prune(ctx context.Context, before time.Time) (int, error) {
	res, err := w.db.ExecContext(ctx, `DELETE FROM events WHERE at < ? AND id < (SELECT min_event_id FROM floor WHERE id = 1)`, ms(before))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// Mutes lists the mutes still in force at now.
func (w *Writer) Mutes(ctx context.Context, now time.Time) ([]domain.FeedMute, error) {
	return queryMutes(ctx, w.db, now)
}

func queryMutes(ctx context.Context, db *sql.DB, now time.Time) ([]domain.FeedMute, error) {
	rows, err := db.QueryContext(ctx, `SELECT scope, key, until, action_id FROM mutes WHERE until > ? ORDER BY scope, key`, ms(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.FeedMute
	for rows.Next() {
		var m domain.FeedMute
		var scope string
		var until int64
		if err := rows.Scan(&scope, &m.Key, &until, &m.ActionID); err != nil {
			return nil, err
		}
		m.Scope = domain.FeedMuteScope(scope)
		m.Until = fromMS(until)
		out = append(out, m)
	}
	return out, rows.Err()
}

// Reader is serve's read-only handle. It never migrates.
type Reader struct{ db *sql.DB }

// OpenReader opens the database read-only. It fails with ErrNoDatabase when the
// file does not exist yet.
func OpenReader(path string) (*Reader, error) {
	if err := requireFile(path); err != nil {
		return nil, err
	}
	// journal_mode is left to the writer: a read-only connection cannot set it.
	u := url.URL{Scheme: "file", Path: path}
	d := fmt.Sprintf("%s?mode=ro&_pragma=busy_timeout(%d)", u.String(), busyTimeout)
	db, err := sql.Open("sqlite", d)
	if err != nil {
		return nil, fmt.Errorf("open feed database reader: %w", err)
	}
	db.SetMaxOpenConns(readerConns)
	db.SetConnMaxIdleTime(time.Minute)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Reader{db: db}, nil
}

// Close closes the pool.
func (r *Reader) Close() error { return r.db.Close() }

// DB is the underlying read-only pool.
func (r *Reader) DB() *sql.DB { return r.db }

const eventCols = `id, source, external_id, at, actor, actor_name, kind, title, body, url, thread_key, class, urgent, own, enrichment, channel`

func scanEvents(rows *sql.Rows) ([]domain.FeedEvent, error) {
	defer rows.Close()
	var out []domain.FeedEvent
	for rows.Next() {
		var ev domain.FeedEvent
		var source, class string
		var at int64
		var urgent, own int
		if err := rows.Scan(&ev.ID, &source, &ev.ExternalID, &at, &ev.Actor, &ev.ActorName, &ev.Kind, &ev.Title, &ev.Body,
			&ev.URL, &ev.ThreadKey, &class, &urgent, &own, &ev.Enrichment, &ev.Channel); err != nil {
			return nil, err
		}
		ev.Source = domain.FeedSource(source)
		ev.Class = domain.FeedEventClass(class)
		ev.At = fromMS(at)
		ev.Urgent = urgent != 0
		ev.Own = own != 0
		out = append(out, ev)
	}
	return out, rows.Err()
}

// After returns up to limit events with an id above afterID, oldest first.
func (r *Reader) After(ctx context.Context, afterID int64, limit int) ([]domain.FeedEvent, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+eventCols+` FROM events WHERE id > ? ORDER BY id ASC LIMIT ?`, afterID, limit)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

// ByIDs returns the events with these ids, oldest first. Unknown ids are skipped.
func (r *Reader) ByIDs(ctx context.Context, ids []int64) ([]domain.FeedEvent, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+eventCols+` FROM events WHERE id IN (`+marks+`) ORDER BY id ASC`, args...)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

// Thread returns the newest limit events of a thread with an id below beforeID,
// oldest first. A beforeID of 0 or less means no upper bound.
func (r *Reader) Thread(ctx context.Context, threadKey string, beforeID int64, limit int) ([]domain.FeedEvent, error) {
	if beforeID <= 0 {
		beforeID = 1<<63 - 1
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+eventCols+` FROM (
		SELECT `+eventCols+` FROM events WHERE thread_key = ? AND id < ? ORDER BY id DESC LIMIT ?) ORDER BY id ASC`,
		threadKey, beforeID, limit)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

// Health lists every source's last poll outcome.
func (r *Reader) Health(ctx context.Context) ([]Health, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT source, COALESCE(last_poll,0), COALESCE(last_ok,0), error, calls FROM health ORDER BY source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Health
	for rows.Next() {
		var h Health
		var source string
		var poll, ok int64
		if err := rows.Scan(&source, &poll, &ok, &h.Error, &h.Calls); err != nil {
			return nil, err
		}
		h.Source = domain.FeedSource(source)
		h.LastPoll, h.LastOK = fromMS(poll), fromMS(ok)
		out = append(out, h)
	}
	return out, rows.Err()
}

// Mutes lists the mutes still in force at now.
func (r *Reader) Mutes(ctx context.Context, now time.Time) ([]domain.FeedMute, error) {
	return queryMutes(ctx, r.db, now)
}

// MuteWriter is serve's only write handle. It writes mutes and the prune floor
// and never migrates.
type MuteWriter struct{ db *sql.DB }

// OpenMuteWriter opens the existing database for writing mutes and the floor.
func OpenMuteWriter(path string) (*MuteWriter, error) {
	if err := requireFile(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn(path, "_txlock=immediate"))
	if err != nil {
		return nil, fmt.Errorf("open feed database mute writer: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &MuteWriter{db: db}, nil
}

// Close closes the handle.
func (m *MuteWriter) Close() error { return m.db.Close() }

// Mute sets or replaces a mute.
func (m *MuteWriter) Mute(ctx context.Context, mute domain.FeedMute) error {
	_, err := m.db.ExecContext(ctx, `INSERT INTO mutes(scope, key, until, action_id) VALUES(?,?,?,?)
		ON CONFLICT(scope, key) DO UPDATE SET until = excluded.until, action_id = excluded.action_id`,
		string(mute.Scope), mute.Key, ms(mute.Until), mute.ActionID)
	return err
}

// Unmute removes a mute.
func (m *MuteWriter) Unmute(ctx context.Context, scope domain.FeedMuteScope, key string) error {
	_, err := m.db.ExecContext(ctx, `DELETE FROM mutes WHERE scope = ? AND key = ?`, string(scope), key)
	return err
}

// SetFloor sets the prune floor: events with an id below it may be pruned.
func (m *MuteWriter) SetFloor(ctx context.Context, minEventID int64) error {
	_, err := m.db.ExecContext(ctx, `INSERT INTO floor(id, min_event_id) VALUES(1, ?)
		ON CONFLICT(id) DO UPDATE SET min_event_id = excluded.min_event_id`, minEventID)
	return err
}
