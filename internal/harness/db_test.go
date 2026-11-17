package harness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/redact"
)

const testDBFile = `version: 1
app:
  run: exec httpd -f -p 8080
services:
  db:
    image: postgres:16
    env: {POSTGRES_PASSWORD: test}
    ready: pg_isready
    database: {kind: postgres, name: app}
  store:
    image: postgres:16
    database: {kind: sqlite, path: /data/app.db}
    limits: {memory: 256m}
  cache:
    image: postgres:16
    limits: {memory: 256m}
scenarios:
  - id: pg
    services: [db, store, cache]
`

// fakePG records every read and answers with table.
type fakePG struct {
	mu    sync.Mutex
	conns []PGConn
	sqls  []string
	table *DBTable
	err   error
}

func (f *fakePG) Read(_ context.Context, c PGConn, sql string, limit int) (*DBTable, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conns, f.sqls = append(f.conns, c), append(f.sqls, sql)
	if f.err != nil {
		return nil, f.err
	}
	t := *f.table
	if len(t.Rows) > limit {
		t.Rows, t.Truncated = t.Rows[:limit], true
	}
	return &t, nil
}

// fakeSQLiteRunner runs the SQLite reader in process, with /db mapped to dir.
type fakeSQLiteRunner struct {
	dir  string
	args [][]string
}

func (f *fakeSQLiteRunner) RunContainer(_ context.Context, args []string, stdin io.Reader) (string, string, int, error) {
	f.args = append(f.args, args)
	var req sqliteRequest
	if err := json.NewDecoder(stdin).Decode(&req); err != nil {
		return "", err.Error(), 1, nil
	}
	req.File = filepath.Join(f.dir, strings.TrimPrefix(req.File, sqliteReaderMount+"/"))
	b, _ := json.Marshal(req)
	var out bytes.Buffer
	if err := SQLiteReadMain(bytes.NewReader(b), &out); err != nil {
		return "", err.Error(), 1, nil
	}
	return out.String(), "", 0, nil
}

func strp(s string) *string { return &s }

func newDBManager(t *testing.T, pg *fakePG, c *fakeSQLiteRunner) (*testManager, Run) {
	t.Helper()
	tm := newTestManager(t, func(o *Options) {
		o.Postgres = pg
		if c != nil {
			o.Containers = c
		}
	})
	run := tm.upReady(t, testRepo(t, testDBFile), "pg")
	return tm, run
}

func dbCode(err error) string {
	var he *Error
	if errors.As(err, &he) {
		return he.Code
	}
	return ""
}

func TestDBRejectsMultiStatement(t *testing.T) {
	for _, sql := range []string{
		"select 1; select 2",
		"select 1;select pg_sleep(10);",
		"select ';'",
		"COPY orders TO STDOUT",
		"select * into t2 from orders",
		"insert into orders values (1)",
		"update orders set total = 0",
		"set role postgres",
	} {
		if _, err := CheckReadSQL("postgres", sql); dbCode(err) != CodeDBRefused {
			t.Errorf("%q: err = %v, want %s", sql, err, CodeDBRefused)
		}
	}
	for _, sql := range []string{"PRAGMA query_only = OFF", "attach 'x.db' as x"} {
		if _, err := CheckReadSQL("sqlite", sql); dbCode(err) != CodeDBRefused {
			t.Errorf("sqlite %q: err = %v, want %s", sql, err, CodeDBRefused)
		}
	}
	for in, want := range map[string]string{
		"select * from orders":            "select * from orders",
		"  select 1;  ":                   "select 1",
		"select offset_at, reset from t":  "select offset_at, reset from t",
		"explain select * from orders":    "explain select * from orders",
		"select settings from preference": "select settings from preference",
	} {
		got, err := CheckReadSQL("postgres", in)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", in, got, err, want)
		}
	}

	pg := &fakePG{table: &DBTable{}}
	tm, run := newDBManager(t, pg, nil)
	_, err := tm.DB(context.Background(), DBRequest{Run: run.ID, Service: "db", Op: DBOpQuery, SQL: "select 1; drop table orders"})
	if dbCode(err) != CodeDBRefused {
		t.Fatalf("err = %v, want %s", err, CodeDBRefused)
	}
	if len(pg.sqls) != 0 {
		t.Fatalf("a refused statement reached postgres: %q", pg.sqls)
	}
}

func TestDBUsesReadRole(t *testing.T) {
	pg := &fakePG{table: &DBTable{Columns: []string{"id"}, Rows: [][]*string{{strp("42")}}}}
	tm, run := newDBManager(t, pg, nil)
	res, err := tm.DB(context.Background(), DBRequest{Run: run.ID, Service: "db", Op: DBOpRows, Table: "public.orders"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Role != PostgresReadRole || len(pg.conns) != 1 {
		t.Fatalf("role %s, conns %+v", res.Role, pg.conns)
	}
	c := pg.conns[0]
	if c.User != PostgresReadRole || c.Host != "127.0.0.1" || c.Port != 41873 || c.Database != "app" || c.Password == "" {
		t.Fatalf("conn = %+v", c)
	}
	// The password is the run's own, the one the compose file gives the
	// postgres container for aiman_ro.
	compose, err := os.ReadFile(filepath.Join(tm.dir, "s1", run.ID, "sandbox", "compose.yaml"))
	if err != nil || !strings.Contains(string(compose), PostgresReadPasswordEnv+": "+c.Password) {
		t.Fatalf("password %q is not the run's: %v", c.Password, err)
	}
	if pg.sqls[0] != `SELECT * FROM "public"."orders" LIMIT 201` {
		t.Fatalf("rows sql = %q", pg.sqls[0])
	}
	if len(res.Rows) != 1 || *res.Rows[0][0] != "42" {
		t.Fatalf("rows = %v", res.Rows)
	}
	// A read never runs psql as the superuser.
	if n := tm.docker.called("exec -T db psql"); n != 0 {
		t.Fatalf("a read ran psql as the superuser %d times", n)
	}
}

func TestDBReadOnly(t *testing.T) {
	// Postgres: the session is read-only under statement_timeout, the
	// statement sits inside BEGIN READ ONLY and the transaction rolls back.
	plan := pgReadPlan("select 1")
	if !slices.Equal(plan, []string{"BEGIN READ ONLY", "select 1", "ROLLBACK"}) {
		t.Fatalf("plan = %q", plan)
	}
	t.Setenv("PGUSER", "postgres")
	t.Setenv("PGPASSWORD", "superuser-password")
	t.Setenv("PGOPTIONS", "-c default_transaction_read_only=off")
	cfg, err := pgReadConfig(PGConn{Host: "127.0.0.1", Port: 5555, User: PostgresReadRole, Password: "ro", Database: "app"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.User != PostgresReadRole || cfg.Password != "ro" || cfg.Port != 5555 || cfg.TLSConfig != nil {
		t.Fatalf("config = user %s port %d", cfg.User, cfg.Port)
	}
	want := map[string]string{"application_name": "aiman-harness-db", "default_transaction_read_only": "on", "statement_timeout": "5000"}
	if len(cfg.RuntimeParams) != len(want) {
		t.Fatalf("runtime params = %v", cfg.RuntimeParams)
	}
	for k, v := range want {
		if cfg.RuntimeParams[k] != v {
			t.Fatalf("runtime params = %v", cfg.RuntimeParams)
		}
	}

	// SQLite: the reader mounts the volume read-only, has no network and a
	// read-only root, and opens the file mode=ro and query_only.
	args := strings.Join(sqliteReaderArgs(Run{ID: "r1"}, "store", "sha256:edge", "/bin/aiman", false), " ")
	for _, want := range []string{
		"--network none", "--read-only", "--cap-drop ALL", "--cap-add DAC_READ_SEARCH", "--pull never",
		"-v harness-r1_data-store:/db:ro", "-v /bin/aiman:/aiman:ro", "no-new-privileges:true", "sha256:edge harness db-read",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("reader args lack %q: %s", want, args)
		}
	}
	if strings.Contains(args, "DAC_OVERRIDE") {
		t.Errorf("a read may override permissions: %s", args)
	}
	if got := sqliteDSN("/db/app.db", false, false); got != "file:/db/app.db?mode=ro" {
		t.Fatalf("dsn = %s", got)
	}
	file := sqliteFixture(t)
	for _, stmt := range []string{"insert into orders values (2, 'x', NULL)", "delete from orders", "create table x (id int)"} {
		if _, err := sqliteRun(context.Background(), sqliteRequest{File: file, SQL: stmt, Limit: 10}); err == nil {
			t.Fatalf("read connection ran %q", stmt)
		}
	}
	tab, err := sqliteRun(context.Background(), sqliteRequest{File: file, SQL: "select count(*) from orders", Limit: 10})
	if err != nil || *tab.Rows[0][0] != "1" {
		t.Fatalf("count = %+v, %v", tab, err)
	}
}

// sqliteFixture is a database with one orders row, writable on disk so only
// the reader's own connection keeps it read-only.
func sqliteFixture(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "app.db")
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range []string{
		"create table orders (id integer, status text, api_key text)",
		"insert into orders values (42, 'paid', 'sk-live-abcdefghijklmnopqrstuvwxyz')",
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	return file
}

func TestDBRedactsColumns(t *testing.T) {
	pg := &fakePG{table: &DBTable{
		Columns: []string{"id", "Password_Hash", "api_key", "note", "deleted_at"},
		Rows: [][]*string{
			{strp("1"), strp("$2a$10$abc"), strp("plain"), strp("call with Bearer abcdef0123456789"), nil},
			{strp("2"), nil, strp("x"), strp("line one\npassword=hunter2"), nil},
		},
	}}
	tm, run := newDBManager(t, pg, nil)
	res, err := tm.DB(context.Background(), DBRequest{Run: run.ID, Service: "db", Op: DBOpQuery, SQL: "select * from users"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.RedactedColumns, []string{"Password_Hash", "api_key"}) {
		t.Fatalf("redacted columns = %v", res.RedactedColumns)
	}
	r0, r1 := res.Rows[0], res.Rows[1]
	if *r0[1] != redact.Mark || *r0[2] != redact.Mark || *r1[2] != redact.Mark {
		t.Fatalf("secret columns kept values: %v %v %v", *r0[1], *r0[2], *r1[2])
	}
	if r1[1] != nil || r0[4] != nil {
		t.Fatal("NULL became a value")
	}
	if strings.Contains(*r0[3], "abcdef0123456789") || strings.Contains(*r1[3], "hunter2") || !strings.HasPrefix(*r1[3], "line one\n") {
		t.Fatalf("cells not redacted: %q %q", *r0[3], *r1[3])
	}
	if *r0[0] != "1" || res.Redacted < 5 {
		t.Fatalf("id %q, redacted %d", *r0[0], res.Redacted)
	}

	// The row limit caps a read and marks it truncated.
	pg.table = &DBTable{Columns: []string{"id"}, Rows: [][]*string{{strp("1")}, {strp("2")}, {strp("3")}}}
	res, err = tm.DB(context.Background(), DBRequest{Run: run.ID, Service: "db", Op: DBOpQuery, SQL: "select id from t", Limit: 2})
	if err != nil || len(res.Rows) != 2 || !res.Truncated || res.Limit != 2 {
		t.Fatalf("limited read = %+v, %v", res, err)
	}

	// SQLite cells pass the same redaction.
	c := &fakeSQLiteRunner{dir: filepath.Dir(sqliteFixture(t))}
	tm2, run2 := newDBManager(t, &fakePG{}, c)
	res, err = tm2.DB(context.Background(), DBRequest{Run: run2.ID, Service: "store", Op: DBOpRows, Table: "orders"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Role != DBRoleSQLiteRead || len(res.Rows) != 1 || *res.Rows[0][0] != "42" || *res.Rows[0][2] != redact.Mark {
		t.Fatalf("sqlite rows = %+v", res)
	}
	tabs, err := tm2.DB(context.Background(), DBRequest{Run: run2.ID, Service: "store", Op: DBOpTables})
	if err != nil || !slices.Equal(tabs.Tables, []string{"orders"}) {
		t.Fatalf("sqlite tables = %v, %v", tabs.Tables, err)
	}
	if args := strings.Join(c.args[0], " "); !strings.Contains(args, "harness-"+run2.ID+"_data-store:/db:ro") {
		t.Fatalf("reader args = %s", args)
	}
}

func TestDBWriteNeedsOperator(t *testing.T) {
	pg := &fakePG{table: &DBTable{}}
	tm, run := newDBManager(t, pg, nil)
	stmt := "update orders set status = 'paid' where id = 42"
	before := tm.docker.called("exec -T db psql")
	_, err := tm.DB(context.Background(), DBRequest{Run: run.ID, Service: "db", Op: DBOpQuery, SQL: stmt, Write: true, Actor: domain.EventActorAgent})
	if dbCode(err) != CodeNeedsOperator {
		t.Fatalf("err = %v, want %s", err, CodeNeedsOperator)
	}
	if tm.docker.called("exec -T db psql") != before || len(pg.sqls) != 0 || tm.rec.last(domain.EventHarnessDBWrite) != nil {
		t.Fatal("a refused write ran")
	}

	tm.docker.answer = func(args []string) (string, int, bool) {
		if slices.Contains(args, "psql") {
			return "UPDATE 1\n", 0, true
		}
		return "", 0, false
	}
	res, err := tm.DB(context.Background(), DBRequest{Run: run.ID, Service: "db", SQL: stmt, Write: true, Operator: true, Actor: domain.EventActorUser})
	if err != nil {
		t.Fatal(err)
	}
	if res.Role != DBRoleOwner || res.Tag != "UPDATE 1" {
		t.Fatalf("write = %+v", res)
	}
	if tm.docker.called("exec -T db psql -X -v ON_ERROR_STOP=1 -U postgres -d app") != 1 {
		t.Fatalf("calls = %q", tm.docker.calls)
	}
	if got := tm.docker.stdin[len(tm.docker.stdin)-1]; got != stmt {
		t.Fatalf("stdin = %q", got)
	}
	row := tm.rec.last(domain.EventHarnessDBWrite)
	sum := sha256.Sum256([]byte(stmt))
	if row["statement_sha256"] != hex.EncodeToString(sum[:]) || row["service"] != "db" || row["run"] != run.ID {
		t.Fatalf("ledger row = %v", row)
	}
	b, _ := json.Marshal(row)
	if strings.Contains(string(b), "orders") {
		t.Fatalf("ledger holds the statement: %s", b)
	}
}

func TestDBUnsupportedAndParams(t *testing.T) {
	tm, run := newDBManager(t, &fakePG{table: &DBTable{}}, nil)
	if _, err := tm.DB(context.Background(), DBRequest{Run: run.ID, Service: "cache", Op: DBOpTables}); dbCode(err) != CodeDBUnsupported {
		t.Fatalf("no database: %v", err)
	}
	if _, err := tm.DB(context.Background(), DBRequest{Run: run.ID, Service: "nope", Op: DBOpTables}); !errors.Is(err, ErrParams) {
		t.Fatalf("unknown service: %v", err)
	}
	if _, err := tm.DB(context.Background(), DBRequest{Run: run.ID, Service: "db", Op: "drop"}); !errors.Is(err, ErrParams) {
		t.Fatalf("bad op: %v", err)
	}
	if _, err := tm.DB(context.Background(), DBRequest{Run: "gone", Service: "db", Op: DBOpTables}); dbCode(err) != CodeNotRunning {
		t.Fatalf("no run: %v", err)
	}
	if !slices.Equal(run.Databases, []RunDatabase{{Service: "db", Kind: "postgres"}, {Service: "store", Kind: "sqlite"}}) {
		t.Fatalf("databases = %+v", run.Databases)
	}
}

func TestDBSQLiteServiceFileAndCompose(t *testing.T) {
	bad := map[string]string{
		"{kind: sqlite}":                          "database.path is required",
		"{kind: sqlite, path: data/app.db}":       "clean absolute path",
		"{kind: sqlite, path: /app.db}":           "directory of its own",
		"{kind: sqlite, path: /tmp/app.db}":       "directory of its own",
		"{kind: sqlite, path: /data/../x/app.db}": "clean absolute path",
		"{kind: sqlite, path: /d/a.db, name: x}":  "sqlite takes path",
		"{kind: postgres, path: /d/a.db}":         "postgres takes name",
	}
	for db, want := range bad {
		file := strings.Replace(testDBFile, "{kind: sqlite, path: /data/app.db}", db, 1)
		if _, err := Parse([]byte(file)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", db, err, want)
		}
	}
	f, err := Parse([]byte(testDBFile))
	if err != nil {
		t.Fatal(err)
	}
	inner, outer := subnets(1)
	p, err := Compose(f, RunSpec{
		RunID: "r1", Scenario: "pg", Src: "/s", Out: "/o", ControlDir: "/c", AimanBin: "/a", UID: 1000, GID: 1000,
		AppImage: "golang:1.25", EdgeImage: EdgeImage, InnerSubnet: inner, OuterSubnet: outer, ReadPassword: "pw",
	})
	if err != nil {
		t.Fatal(err)
	}
	y := string(p.YAML)
	if !strings.Contains(y, "data-store:/data") || !strings.Contains(y, "data-store: {}") {
		t.Fatalf("sqlite service has no data volume:\n%s", y)
	}
}
