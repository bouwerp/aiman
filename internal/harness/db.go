package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/redact"
)

// Database browser refusal codes.
const (
	// CodeDBUnsupported refuses a service that holds no database the
	// browser reads (postgres or sqlite).
	CodeDBUnsupported = "harness_db_unsupported"
	// CodeDBRefused refuses a read that is not one statement, or that names
	// COPY, INTO or SET (and, for SQLite, ATTACH, DETACH or PRAGMA).
	CodeDBRefused = "harness_db_refused"
	// CodeNeedsOperator refuses a write from anyone but the operator; it is
	// serve's needs_operator.
	CodeNeedsOperator = "needs_operator"
)

// Database browser ops.
const (
	DBOpTables = "tables"
	DBOpRows   = "rows"
	DBOpQuery  = "query"
)

// Database browser bounds.
const (
	// DefaultDBRowLimit is harness.db.row_limit unset.
	DefaultDBRowLimit = 200
	// DBStatementTimeout bounds one read statement (postgres
	// statement_timeout, the SQLite reader's context).
	DBStatementTimeout = 5 * time.Second
	// dbWriteTimeout bounds one operator write.
	dbWriteTimeout = 20 * time.Second
	// dbMaxSQLBytes caps a statement.
	dbMaxSQLBytes = 16 << 10
	// dbMaxCellBytes cuts a cell; dbMaxReplyBytes stops adding rows.
	dbMaxCellBytes  = 4 << 10
	dbMaxReplyBytes = 1 << 20
	// dbMaxTables caps a tables listing.
	dbMaxTables = 1000
)

// DBRequest is one harness.db call against a ready run.
type DBRequest struct {
	Run     string
	Service string
	// Op is DBOpTables, DBOpRows (Table set) or DBOpQuery (SQL set).
	Op    string
	Table string
	SQL   string
	// Limit caps the rows; zero or more than harness.db.row_limit is the
	// row limit.
	Limit int
	// Write runs SQL as the database's owner. Only the operator may.
	Write    bool
	Operator bool
	Actor    domain.EventActor
}

// DBTable is what a reader returns: column names and text cells (nil is
// NULL). Truncated is set when more rows existed than were read; Tag is a
// write's command tag.
type DBTable struct {
	Columns   []string    `json:"columns"`
	Rows      [][]*string `json:"rows"`
	Truncated bool        `json:"truncated,omitempty"`
	Tag       string      `json:"tag,omitempty"`
}

// DBResult is the harness_db reply. Role is who read or wrote: aiman_ro for
// a postgres read, read_only for a SQLite read, owner for a write. Every
// cell, output line and table name has passed redaction.
type DBResult struct {
	Run             string      `json:"run"`
	Service         string      `json:"service"`
	Kind            string      `json:"kind"`
	Op              string      `json:"op"`
	Role            string      `json:"role"`
	Write           bool        `json:"write,omitempty"`
	Limit           int         `json:"limit"`
	Tables          []string    `json:"tables,omitempty"`
	Columns         []string    `json:"columns,omitempty"`
	Rows            [][]*string `json:"rows,omitempty"`
	Truncated       bool        `json:"truncated"`
	Redacted        int         `json:"redacted"`
	RedactedColumns []string    `json:"redacted_columns,omitempty"`
	Output          []string    `json:"output,omitempty"`
	Tag             string      `json:"tag,omitempty"`
}

// Roles a DBResult names.
const (
	DBRoleSQLiteRead = "read_only"
	DBRoleOwner      = "owner"
)

var (
	dbRefusedWords       = regexp.MustCompile(`(?i)\b(COPY|INTO|SET)\b`)
	dbSQLiteRefusedWords = regexp.MustCompile(`(?i)\b(ATTACH|DETACH|PRAGMA)\b`)
	// dbSecretColumn names a column whose every value is redacted.
	dbSecretColumn = regexp.MustCompile(`(?i)password|secret|token|api_key`)
)

func dbRefused(msg string) error { return &Error{Code: CodeDBRefused, Msg: msg} }

// CheckReadSQL applies the one-statement rule to a read: one statement (a
// single trailing semicolon is dropped), no semicolon inside it, and none
// of the words COPY, INTO or SET; SQLite also refuses ATTACH, DETACH and
// PRAGMA, which could undo its read-only connection. It returns the
// statement to run.
func CheckReadSQL(kind, sql string) (string, error) {
	s := strings.TrimSpace(sql)
	s = strings.TrimSpace(strings.TrimSuffix(s, ";"))
	switch {
	case s == "":
		return "", paramsError("sql is empty")
	case len(s) > dbMaxSQLBytes:
		return "", paramsError(fmt.Sprintf("sql is over %d bytes", dbMaxSQLBytes))
	case strings.ContainsRune(s, 0):
		return "", dbRefused("sql holds a NUL byte")
	case strings.Contains(s, ";"):
		return "", dbRefused("a read runs one statement only; remove the ;")
	}
	if w := dbRefusedWords.FindString(s); w != "" {
		return "", dbRefused(fmt.Sprintf("a read may not use %s; ask the operator for a write", strings.ToUpper(w)))
	}
	if kind == "sqlite" {
		if w := dbSQLiteRefusedWords.FindString(s); w != "" {
			return "", dbRefused(fmt.Sprintf("a SQLite read may not use %s", strings.ToUpper(w)))
		}
	}
	return s, nil
}

// quoteIdent quotes one SQL identifier.
func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// tableRef quotes a table name: schema.table for postgres (no dot is the
// search path), the whole name for SQLite.
func tableRef(kind, table string) (string, error) {
	t := strings.TrimSpace(table)
	if t == "" || len(t) > 256 || strings.ContainsRune(t, 0) {
		return "", paramsError("name a table")
	}
	if kind == "postgres" {
		if schema, name, ok := strings.Cut(t, "."); ok {
			return quoteIdent(schema) + "." + quoteIdent(name), nil
		}
	}
	return quoteIdent(t), nil
}

const (
	pgTablesSQL = `SELECT table_schema || '.' || table_name FROM information_schema.tables ` +
		`WHERE table_schema NOT IN ('pg_catalog', 'information_schema') ORDER BY 1`
	sqliteTablesSQL = `SELECT name FROM sqlite_schema WHERE type IN ('table', 'view') ` +
		`AND name NOT LIKE 'sqlite_%' ORDER BY name`
)

// DB answers one database browser call on a ready run. Reads go to postgres
// as aiman_ro through the edge relay, in a read-only transaction under
// statement_timeout, or to a one-shot SQLite reader container that mounts
// the data volume read-only. A write runs as the database's owner, only for
// the operator, and records harness.db_write with the statement's sha256.
func (m *Manager) DB(ctx context.Context, req DBRequest) (DBResult, error) {
	if req.Write && !req.Operator {
		return DBResult{}, &Error{Code: CodeNeedsOperator, Msg: "only the operator writes to a harness database"}
	}
	lr, err := m.readyRunOf(req.Run)
	if err != nil {
		return DBResult{}, err
	}
	m.Touch(req.Run)
	lr.mu.Lock()
	run, f, password := lr.run, lr.file, lr.readPassword
	lr.mu.Unlock()
	db, err := runDatabase(run, f, req.Service)
	if err != nil {
		return DBResult{}, err
	}
	limit := m.opts.DBRowLimit
	if req.Limit > 0 && req.Limit < limit {
		limit = req.Limit
	}
	res := DBResult{Run: run.ID, Service: req.Service, Kind: db.Kind, Op: req.Op, Limit: limit, Write: req.Write}
	sql, err := dbStatement(db.Kind, req, limit)
	if err != nil {
		return DBResult{}, err
	}
	readLimit := limit
	if req.Op == DBOpTables {
		readLimit = dbMaxTables
	}
	var t *DBTable
	switch {
	case req.Write:
		res.Role = DBRoleOwner
		t, err = m.dbWrite(ctx, run, f, req.Service, db, sql)
	case db.Kind == "postgres":
		res.Role = PostgresReadRole
		t, err = m.pgRead(ctx, run, f, req.Service, password, sql, readLimit)
	default:
		res.Role = DBRoleSQLiteRead
		t, err = m.sqliteCall(ctx, run, req.Service, db, sqliteRequest{SQL: sql, Limit: readLimit})
	}
	if err != nil {
		return DBResult{}, err
	}
	if req.Write {
		sum := sha256.Sum256([]byte(sql))
		m.record(run.Session, req.Actor, domain.EventHarnessDBWrite, map[string]any{
			"run": run.ID, "service": req.Service, "statement_sha256": hex.EncodeToString(sum[:]),
		})
	}
	fillResult(&res, t)
	return res, nil
}

// runDatabase is the database of a run's service.
func runDatabase(run Run, f *File, service string) (Database, error) {
	if !slices.Contains(run.Services, service) {
		return Database{}, paramsError(fmt.Sprintf("run %s has no service %q", run.ID, service))
	}
	if f == nil {
		return Database{}, fmt.Errorf("run %s has no harness file loaded", run.ID)
	}
	db := f.Services[service].Database
	if db == nil || (db.Kind != "postgres" && db.Kind != "sqlite") {
		return Database{}, &Error{Code: CodeDBUnsupported, Msg: fmt.Sprintf("service %s holds no postgres or sqlite database", service)}
	}
	return *db, nil
}

// dbStatement is the SQL a call runs: aiman's own for tables and rows, the
// caller's for a query (checked by the one-statement rule unless it is the
// operator's write).
func dbStatement(kind string, req DBRequest, limit int) (string, error) {
	if req.Write {
		if req.Op != "" && req.Op != DBOpQuery {
			return "", paramsError("a write is op query with sql")
		}
		s := strings.TrimSpace(req.SQL)
		if s == "" || len(s) > dbMaxSQLBytes {
			return "", paramsError(fmt.Sprintf("a write needs sql of at most %d bytes", dbMaxSQLBytes))
		}
		return s, nil
	}
	switch req.Op {
	case DBOpTables:
		if kind == "postgres" {
			return pgTablesSQL, nil
		}
		return sqliteTablesSQL, nil
	case DBOpRows:
		ref, err := tableRef(kind, req.Table)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("SELECT * FROM %s LIMIT %d", ref, limit+1), nil
	case DBOpQuery:
		return CheckReadSQL(kind, req.SQL)
	}
	return "", paramsError("op must be tables, rows or query")
}

func (m *Manager) pgRead(ctx context.Context, run Run, f *File, service, password, sql string, limit int) (*DBTable, error) {
	port := 0
	for _, p := range run.Ports {
		if p.Name == service && p.Service == service {
			port = p.HostPort
		}
	}
	if port == 0 {
		return nil, fmt.Errorf("run %s publishes no relay for %s", run.ID, service)
	}
	if password == "" {
		return nil, fmt.Errorf("run %s has no read password; it was started by an earlier serve", run.ID)
	}
	_, db := postgresUserDB(f.Services[service])
	return m.opts.Postgres.Read(ctx, PGConn{
		Host: "127.0.0.1", Port: port, User: PostgresReadRole, Password: password, Database: db,
	}, sql, limit)
}

// postgresUserDB is the superuser and database a postgres service's image
// creates: POSTGRES_USER (postgres), and database.name, else POSTGRES_DB,
// else the user.
func postgresUserDB(svc Service) (user, db string) {
	user = svc.Env["POSTGRES_USER"]
	if user == "" {
		user = "postgres"
	}
	if svc.Database != nil {
		db = svc.Database.Name
	}
	if db == "" {
		db = svc.Env["POSTGRES_DB"]
	}
	if db == "" {
		db = user
	}
	return user, db
}

// dbWrite runs the operator's statement as the owner: psql in the postgres
// container over its local socket, or the SQLite reader with the volume
// mounted read-write.
func (m *Manager) dbWrite(ctx context.Context, run Run, f *File, service string, db Database, sql string) (*DBTable, error) {
	ctx, cancel := context.WithTimeout(ctx, dbWriteTimeout)
	defer cancel()
	if db.Kind == "sqlite" {
		return m.sqliteCall(ctx, run, service, db, sqliteRequest{SQL: sql, Write: true})
	}
	user, name := postgresUserDB(f.Services[service])
	out, exit, err := m.opts.Docker.Compose(ctx, ComposeCall{
		Dir: filepath.Join(m.runDir(run.Session, run.ID), "sandbox"), Project: "harness-" + run.ID,
		Args:  []string{"exec", "-T", service, "psql", "-X", "-v", "ON_ERROR_STOP=1", "-U", user, "-d", name},
		Stdin: strings.NewReader(sql),
	})
	if err != nil {
		return nil, err
	}
	out = strings.TrimRight(out, "\n")
	if exit != 0 {
		return nil, dbQueryError(fmt.Sprintf("psql exit %d: %s", exit, tailLines(out, 5)))
	}
	return &DBTable{Tag: out}, nil
}

// fillResult copies a reader's table into res: table names for a tables
// listing, else columns and rows, redacting secret columns and every cell,
// cutting long cells and stopping at dbMaxReplyBytes.
func fillResult(res *DBResult, t *DBTable) {
	res.Truncated = t.Truncated
	if t.Tag != "" {
		lines, n := redact.Lines(strings.Split(t.Tag, "\n"), nil)
		res.Redacted += n
		res.Output = lines
		res.Tag = lines[len(lines)-1]
	}
	if res.Op == DBOpTables && !res.Write {
		res.Tables = []string{}
		for _, r := range t.Rows {
			if len(r) > 0 && r[0] != nil {
				name, n := redact.Line(*r[0], nil)
				res.Redacted += n
				res.Tables = append(res.Tables, name)
			}
		}
		return
	}
	if len(t.Columns) == 0 {
		return
	}
	res.Columns = t.Columns
	secret := make([]bool, len(t.Columns))
	for i, c := range t.Columns {
		if dbSecretColumn.MatchString(c) {
			secret[i] = true
			res.RedactedColumns = append(res.RedactedColumns, c)
		}
	}
	res.Rows = [][]*string{}
	size := 0
	for _, r := range t.Rows {
		if len(res.Rows) >= res.Limit {
			res.Truncated = true
			break
		}
		row := make([]*string, len(r))
		for i, cell := range r {
			if cell == nil {
				continue
			}
			v := *cell
			if i < len(secret) && secret[i] {
				v = redact.Mark
				res.Redacted++
			} else {
				lines, n := redact.Lines(strings.Split(v, "\n"), nil)
				res.Redacted += n
				v = cutCell(strings.Join(lines, "\n"))
			}
			size += len(v)
			row[i] = &v
		}
		if size > dbMaxReplyBytes {
			res.Truncated = true
			break
		}
		res.Rows = append(res.Rows, row)
	}
}

// cutCell cuts v to dbMaxCellBytes on a rune boundary.
func cutCell(v string) string {
	if len(v) <= dbMaxCellBytes {
		return v
	}
	cut := dbMaxCellBytes
	for cut > 0 && !utf8.RuneStart(v[cut]) {
		cut--
	}
	return v[:cut] + "…"
}

// dbQueryError is a database's own refusal of a statement, redacted: the
// caller's mistake, answered as invalid_params.
func dbQueryError(msg string) error {
	red, _ := redact.Line(msg, nil)
	return paramsError("the database refused the statement: " + red)
}
