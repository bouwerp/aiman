package harness

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestDBPostgresReadRoleDocker reads a real postgres service through the
// edge relay as aiman_ro: reads work, and both the read-only transaction and
// the role itself refuse writes.
func TestDBPostgresReadRoleDocker(t *testing.T) {
	ensureImage(t, dockerTestPostgres)
	r := upRun(t, dockerPostgresFile, "pg")
	if out, err := r.exec("db", `psql -U postgres -d app -c "create table orders (id int, api_key text); insert into orders values (42, 'k')"`); err != nil {
		t.Fatalf("superuser setup: %v: %s", err, out)
	}
	_, portStr, _ := strings.Cut(r.hostPort(FirstRelayPort), ":")
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("relay port %q", portStr)
	}
	conn := PGConn{Host: "127.0.0.1", Port: port, User: PostgresReadRole, Password: r.spec.ReadPassword, Database: "app"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	tab, err := PGReader{}.Read(ctx, conn, "select id from orders", 10)
	if err != nil || len(tab.Rows) != 1 || *tab.Rows[0][0] != "42" {
		t.Fatalf("read = %+v, %v", tab, err)
	}
	tab, err = PGReader{}.Read(ctx, conn, pgTablesSQL, 10)
	if err != nil || len(tab.Rows) != 1 || *tab.Rows[0][0] != "public.orders" {
		t.Fatalf("tables = %+v, %v", tab, err)
	}
	for _, stmt := range []string{"insert into orders values (1, 'x')", "create table x (id int)", "delete from orders"} {
		if _, err := (PGReader{}).Read(ctx, conn, stmt, 10); err == nil || !strings.Contains(err.Error(), "read-only transaction") {
			t.Fatalf("%q through the reader: %v", stmt, err)
		}
	}
	start := time.Now()
	if _, err := (PGReader{}).Read(ctx, conn, "select pg_sleep(30)", 10); err == nil || !strings.Contains(err.Error(), "statement timeout") {
		t.Fatalf("long read: %v", err)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatalf("statement_timeout did not stop the read: %s", time.Since(start))
	}

	// Without the read-only session the role still cannot write.
	cfg, err := pgReadConfig(conn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeParams = map[string]string{}
	raw, err := pgconn.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close(context.Background())
	_, err = raw.Exec(ctx, "insert into orders values (1, 'x')").ReadAll()
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("aiman_ro wrote outside a read-only transaction: %v", err)
	}
}

// TestDBSQLiteReaderDocker runs the real reader container over a data
// volume: it reads the file, and its read-only mount refuses a write even
// when the request asks for one.
func TestDBSQLiteReaderDocker(t *testing.T) {
	needDocker(t)
	ensureImage(t, dockerTestImage)
	bin := edgeBinary(t)
	id := make([]byte, 5)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	run := Run{ID: "t" + hex.EncodeToString(id)}
	vol := DataVolumeName(run.ID, "store")
	if out, err := exec.Command("docker", "volume", "create", vol).CombinedOutput(); err != nil {
		t.Fatalf("volume: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "-f", vol).Run() })
	file := sqliteFixture(t)
	if out, err := exec.Command("docker", "run", "--rm", "-v", vol+":/db", "-v", filepath.Dir(file)+":/src:ro",
		dockerTestImage, "cp", "/src/app.db", "/db/app.db").CombinedOutput(); err != nil {
		t.Fatalf("seed volume: %v: %s", err, out)
	}
	call := func(req sqliteRequest, write bool) sqliteReply {
		t.Helper()
		req.File = sqliteReaderMount + "/app.db"
		in, _ := json.Marshal(req)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		stdout, stderr, exit, err := ExecDocker{}.RunContainer(ctx, sqliteReaderArgs(run, "store", dockerTestImage, bin, write), bytes.NewReader(in))
		if err != nil {
			t.Fatal(err)
		}
		var rep sqliteReply
		if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
			t.Fatalf("reader exit %d: %s %s", exit, stdout, stderr)
		}
		return rep
	}
	rep := call(sqliteRequest{SQL: "select id, status from orders", Limit: 10}, false)
	if rep.Error != "" || len(rep.Table.Rows) != 1 || *rep.Table.Rows[0][1] != "paid" {
		t.Fatalf("read = %+v", rep)
	}
	// A write request on the read-only mount fails at the filesystem.
	if rep := call(sqliteRequest{SQL: "delete from orders", Write: true}, false); rep.Error == "" {
		t.Fatalf("write through a read-only mount: %+v", rep.Table)
	}
	// The operator's write mounts it read-write.
	if rep := call(sqliteRequest{SQL: "update orders set status = 'refunded'", Write: true}, true); rep.Error != "" || rep.Table.Tag != "rows affected 1" {
		t.Fatalf("write = %+v", rep)
	}
	if rep := call(sqliteRequest{SQL: "select status from orders", Limit: 10}, false); rep.Error != "" || *rep.Table.Rows[0][0] != "refunded" {
		t.Fatalf("read after write = %+v", rep)
	}
}

const dockerDBManagerFile = `version: 1
app:
  run: exec sleep 600
services:
  db:
    image: postgres:16
    env: {POSTGRES_PASSWORD: test}
    ready: pg_isready -h 127.0.0.1 -U postgres
    database: {kind: postgres, name: app}
    root: true
    caps: [CHOWN, SETUID, SETGID, DAC_OVERRIDE, FOWNER]
    limits: {memory: 1g}
scenarios:
  - id: pg
    services: [db]
    fixtures: [{sql: testdata/orders.sql, into: db}]
`

// TestManagerDockerDB runs a real scenario and browses its database through
// the manager: tables, rows with a redacted column, a refused second
// statement, and the operator's write.
func TestManagerDockerDB(t *testing.T) {
	needDocker(t)
	ensureImage(t, dockerTestImage)
	ensureImage(t, EdgeImage)
	ensureImage(t, dockerTestPostgres)
	repo := testRepo(t, dockerDBManagerFile)
	writeTestFile(t, filepath.Join(repo, "testdata", "orders.sql"),
		"create table orders (id int, status text, api_key text);\ninsert into orders values (42, 'paid', 'k-123');\n")
	gitRun(t, repo, "add", "-A")
	gitRun(t, repo, "commit", "-q", "-m", "orders")
	rec := &recorder{}
	m, err := NewManager(Options{
		Dir:      filepath.Join(t.TempDir(), "harness"),
		AimanBin: edgeBinary(t),
		UID:      os.Getuid(), GID: os.Getgid(),
		AllowedImages: []string{"busybox:*", dockerTestPostgres},
		Docker:        ExecDocker{},
		AppImage:      func(string, string) (string, error) { return dockerTestImage, nil },
		Record:        rec.record,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	run, err := m.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: "pg", Actor: domain.EventActorUser})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "compose", "-p", "harness-"+run.ID, "down", "-v", "--remove-orphans", "-t", "1").Run()
	})
	run = m.Wait(context.Background(), run.ID, 3*time.Minute)
	if run.State != StateReady {
		t.Fatalf("run %s: %s: %s", run.State, run.FailedStep, run.Error)
	}
	ctx := context.Background()
	tabs, err := m.DB(ctx, DBRequest{Run: run.ID, Service: "db", Op: DBOpTables})
	if err != nil || len(tabs.Tables) != 1 || tabs.Tables[0] != "public.orders" {
		t.Fatalf("tables = %+v, %v", tabs, err)
	}
	rows, err := m.DB(ctx, DBRequest{Run: run.ID, Service: "db", Op: DBOpRows, Table: "public.orders"})
	if err != nil || rows.Role != PostgresReadRole || len(rows.Rows) != 1 || *rows.Rows[0][0] != "42" || *rows.Rows[0][2] != "[redacted]" {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
	if _, err := m.DB(ctx, DBRequest{Run: run.ID, Service: "db", Op: DBOpQuery, SQL: "select 1; delete from orders"}); dbCode(err) != CodeDBRefused {
		t.Fatalf("two statements: %v", err)
	}
	if _, err := m.DB(ctx, DBRequest{Run: run.ID, Service: "db", Op: DBOpQuery, SQL: "delete from orders"}); err == nil {
		t.Fatal("a read deleted rows")
	}
	if _, err := m.DB(ctx, DBRequest{Run: run.ID, Service: "db", SQL: "delete from orders", Write: true}); dbCode(err) != CodeNeedsOperator {
		t.Fatalf("session write: %v", err)
	}
	w, err := m.DB(ctx, DBRequest{Run: run.ID, Service: "db", SQL: "update orders set status = 'refunded' where id = 42", Write: true, Operator: true})
	if err != nil || w.Tag != "UPDATE 1" {
		t.Fatalf("operator write = %+v, %v", w, err)
	}
	q, err := m.DB(ctx, DBRequest{Run: run.ID, Service: "db", Op: DBOpQuery, SQL: "select status from orders where id = 42"})
	if err != nil || *q.Rows[0][0] != "refunded" {
		t.Fatalf("read after write = %+v, %v", q, err)
	}
	if rec.last(domain.EventHarnessDBWrite) == nil {
		t.Fatal("no harness.db_write row")
	}
}
