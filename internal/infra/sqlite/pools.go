package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"
)

// ChangeKind tells an OnChange subscriber what a committed write touched.
type ChangeKind int

const (
	// ChangeIdentity is an inserted or deleted session row, a row whose
	// identity or lifecycle columns changed, or a cleared agent identity.
	ChangeIdentity ChangeKind = iota
	// ChangeOther is any other committed write: hook fields, the ledger,
	// usage, notifications, steering notes.
	ChangeOther
)

func (k ChangeKind) String() string {
	if k == ChangeIdentity {
		return "identity"
	}
	return "other"
}

const (
	// readerConns bounds the reader pool. The writer has one connection.
	readerConns = 8
	// readerIdle closes idle readers so an idle connection does not pin the
	// WAL and stop checkpoints from resetting it.
	readerIdle = time.Minute
	// checkpointEvery is the period of the passive WAL checkpoint.
	checkpointEvery = 5 * time.Minute
)

// dsn builds the driver connection string for a database path.
//
// Pragmas are passed in the DSN rather than executed after opening because
// busy_timeout and synchronous are per connection: database/sql pools
// connections and opens more on demand, so a PRAGMA issued once would apply
// only to whichever connection happened to run it. journal_mode(WAL) persists
// in the file; saying it on every connection also converts a file an older
// binary created. The writer adds _txlock=immediate so Begin takes the write
// lock at once instead of failing on upgrade. The driver strips the query from
// the filename itself, so a plain path needs no URI escaping.
func dsn(dbPath string, writer bool) string {
	if strings.ContainsRune(dbPath, '?') {
		return dbPath // a '?' in the path would be read as the query separator
	}
	q := fmt.Sprintf("%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)",
		dbPath, busyTimeout.Milliseconds())
	if writer {
		q += "&_txlock=immediate"
	}
	return q
}

// openWriter opens the single-connection writer. NewRepository opens the
// reader pool with openReader once the schema exists.
func openWriter(dbPath string) (*sql.DB, error) {
	w, err := sql.Open("sqlite", dsn(dbPath, true))
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	w.SetMaxOpenConns(1)
	return w, nil
}

func openReader(dbPath string) (*sql.DB, error) {
	rd, err := sql.Open("sqlite", dsn(dbPath, false))
	if err != nil {
		return nil, fmt.Errorf("failed to open database reader: %w", err)
	}
	rd.SetMaxOpenConns(readerConns)
	rd.SetConnMaxIdleTime(readerIdle)
	return rd, nil
}

// OnChange registers fn to run after every mutating method commits. fn runs
// on the writing goroutine, so it must be quick and must not write.
func (r *Repository) OnChange(fn func(ChangeKind)) {
	if fn == nil {
		return
	}
	r.subMu.Lock()
	defer r.subMu.Unlock()
	r.subs = append(r.subs, fn)
}

func (r *Repository) changed(k ChangeKind) {
	r.subMu.Lock()
	subs := r.subs
	r.subMu.Unlock()
	for _, fn := range subs {
		fn(k)
	}
}

// changedIf reports k when err is nil and passes err through.
func (r *Repository) changedIf(k ChangeKind, err error) error {
	if err == nil {
		r.changed(k)
	}
	return err
}

// Reader returns the bounded reader pool.
func (r *Repository) Reader() *sql.DB {
	return r.rd
}

// DB returns the writer, for packages that own their own tables in the same
// file, such as the context index.
func (r *Repository) DB() *sql.DB {
	return r.db
}

// checkpointLoop runs a passive WAL checkpoint on the writer until stop.
func (r *Repository) checkpointLoop() {
	defer close(r.done)
	t := time.NewTicker(checkpointEvery)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
			if _, err := r.db.Exec("PRAGMA wal_checkpoint(PASSIVE)"); err != nil {
				log.Printf("sqlite: passive checkpoint: %v", err)
			}
		}
	}
}

// Close closes the readers, folds the WAL into the file and closes the
// writer. Another process holding the file makes the checkpoint answer busy;
// that is logged and the close still succeeds.
func (r *Repository) Close() error {
	var err error
	r.closeOnce.Do(func() {
		close(r.stop)
		<-r.done
		rdErr := r.rd.Close()
		var busy, logFrames, done int
		row := r.db.QueryRowContext(context.Background(), "PRAGMA wal_checkpoint(TRUNCATE)")
		if cerr := row.Scan(&busy, &logFrames, &done); cerr != nil {
			log.Printf("sqlite: closing checkpoint: %v", cerr)
		} else if busy != 0 {
			log.Printf("sqlite: closing checkpoint busy (%d of %d frames); another process holds the file", done, logFrames)
		}
		err = r.db.Close()
		if err == nil {
			err = rdErr
		}
	})
	return err
}

// execErr drops an Exec result so a mutating call fits changedIf.
func execErr(_ sql.Result, err error) error { return err }

// exec runs a mutating statement on the writer and reports ChangeOther once
// it succeeds.
func (r *Repository) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	res, err := r.db.ExecContext(ctx, query, args...)
	if err == nil {
		r.changed(ChangeOther)
	}
	return res, err
}

// commit commits a writer transaction and reports ChangeOther once it lands.
func (r *Repository) commit(tx *sql.Tx) error {
	return r.changedIf(ChangeOther, tx.Commit())
}
