package harness

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	// The SQLite reader runs this pure Go driver inside the reader
	// container; the binary is static, as the edge's is.
	_ "modernc.org/sqlite"
)

// ContainerRunner runs one docker run command; args follow "docker run".
// It returns stdout, the exit code, and err only when docker could not run.
type ContainerRunner interface {
	RunContainer(ctx context.Context, args []string, stdin io.Reader) (stdout, stderr string, exit int, err error)
}

// sqliteReaderTimeout bounds one reader container, start included.
const sqliteReaderTimeout = 30 * time.Second

// sqliteReaderMount is where the reader container sees the data volume.
const sqliteReaderMount = "/db"

// sqliteMaxOutput caps what serve reads from the reader container.
const sqliteMaxOutput = 8 << 20

// sqliteRequest is the reader container's stdin.
type sqliteRequest struct {
	File  string `json:"file"`
	SQL   string `json:"sql"`
	Limit int    `json:"limit"`
	Write bool   `json:"write,omitempty"`
}

// sqliteReply is the reader container's stdout.
type sqliteReply struct {
	Table *DBTable `json:"table,omitempty"`
	// Error is the database's refusal of the statement.
	Error string `json:"error,omitempty"`
}

// RunContainer runs docker run with args.
func (ExecDocker) RunContainer(ctx context.Context, args []string, stdin io.Reader) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, "docker", append([]string{"run"}, args...)...) //nolint:gosec // G204: aiman's own docker run arguments
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &limitedBuffer{b: &stdout, n: sqliteMaxOutput}, &limitedBuffer{b: &stderr, n: 64 << 10}
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return stdout.String(), stderr.String(), 0, nil
	case errors.As(err, &ee) && ee.ExitCode() > 0:
		return stdout.String(), stderr.String(), ee.ExitCode(), nil
	default:
		return stdout.String(), stderr.String(), -1, fmt.Errorf("docker run: %w", err)
	}
}

// limitedBuffer keeps the first n bytes and drops the rest.
type limitedBuffer struct {
	b *bytes.Buffer
	n int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.n - l.b.Len(); room > 0 {
		l.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// sqliteReaderArgs is the reader container: the edge's image (busybox,
// vetted as builtin) running aiman's static binary, mounted read-only, with
// no network, a read-only root, every capability dropped but the one that
// reads a file another user owns, and the data volume mounted read-only.
// A write mounts the volume read-write and may override file permissions.
func sqliteReaderArgs(run Run, service, image, bin string, write bool) []string {
	vol := DataVolumeName(run.ID, service) + ":" + sqliteReaderMount
	caps := []string{"--cap-add", "DAC_READ_SEARCH"}
	if write {
		caps = []string{"--cap-add", "DAC_OVERRIDE", "--cap-add", "FOWNER"}
	} else {
		vol += ":ro"
	}
	args := []string{
		"--rm", "-i", "--pull", "never", "--network", "none", "--read-only",
		"--cap-drop", "ALL",
	}
	args = append(args, caps...)
	args = append(args,
		"--security-opt", "no-new-privileges:true", "--user", "0:0",
		"--memory", "256m", "--cpus", "0.5", "--pids-limit", "64",
		"--label", "aiman.harness.run="+run.ID,
		"-v", vol, "-v", bin+":"+EdgeBinPath+":ro",
		"--entrypoint", EdgeBinPath,
		image, "harness", "db-read",
	)
	return args
}

// sqliteCall runs the reader container for one statement.
func (m *Manager) sqliteCall(ctx context.Context, run Run, service string, db Database, req sqliteRequest) (*DBTable, error) {
	image := run.Images[ServiceEdge]
	if image == "" {
		return nil, fmt.Errorf("run %s has no edge image for the SQLite reader", run.ID)
	}
	if m.opts.AimanBin == "" {
		return nil, errors.New("the SQLite reader needs the aiman binary")
	}
	req.File = sqliteReaderMount + "/" + path.Base(db.Path)
	in, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, sqliteReaderTimeout)
	defer cancel()
	stdout, stderr, exit, err := m.opts.Containers.RunContainer(ctx,
		sqliteReaderArgs(run, service, image, m.opts.AimanBin, req.Write), bytes.NewReader(in))
	if err != nil {
		return nil, err
	}
	var rep sqliteReply
	if jerr := json.Unmarshal([]byte(stdout), &rep); jerr != nil || (rep.Table == nil && rep.Error == "") {
		return nil, fmt.Errorf("the SQLite reader exited %d: %s", exit, tailLines(strings.TrimSpace(stderr+"\n"+stdout), 5))
	}
	if rep.Error != "" {
		return nil, dbQueryError(rep.Error)
	}
	return rep.Table, nil
}

// SQLiteReadMain is `aiman harness db-read`, run inside the reader
// container: one request on stdin, one reply on stdout.
func SQLiteReadMain(stdin io.Reader, stdout io.Writer) error {
	var req sqliteRequest
	if err := json.NewDecoder(io.LimitReader(stdin, 1<<20)).Decode(&req); err != nil {
		return fmt.Errorf("read the request: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), DBStatementTimeout)
	defer cancel()
	t, err := sqliteRun(ctx, req)
	rep := sqliteReply{Table: t}
	if err != nil {
		rep = sqliteReply{Error: err.Error()}
	}
	return json.NewEncoder(stdout).Encode(rep)
}

// sqliteDSN opens file read-only (mode=ro) for a read, or read-write for a
// write; immutable is the fallback for a read-only mount of a WAL database
// whose -shm cannot be made.
func sqliteDSN(file string, write, immutable bool) string {
	mode := "ro"
	if write {
		mode = "rw"
	}
	dsn := "file:" + file + "?mode=" + mode
	if immutable {
		dsn += "&immutable=1"
	}
	return dsn
}

// sqliteRun runs one statement. A read's connection is mode=ro and
// query_only, so a write statement fails even when the mount would allow it.
func sqliteRun(ctx context.Context, req sqliteRequest) (*DBTable, error) {
	t, err := sqliteRunOnce(ctx, req, false)
	if err != nil && !req.Write && strings.Contains(err.Error(), "unable to open") {
		return sqliteRunOnce(ctx, req, true)
	}
	return t, err
}

func sqliteRunOnce(ctx context.Context, req sqliteRequest, immutable bool) (*DBTable, error) {
	db, err := sql.Open("sqlite", sqliteDSN(req.File, req.Write, immutable))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if req.Write {
		res, err := conn.ExecContext(ctx, req.SQL)
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		return &DBTable{Tag: "rows affected " + strconv.FormatInt(n, 10)}, nil
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
		return nil, err
	}
	rows, err := conn.QueryContext(ctx, req.SQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	t := &DBTable{Columns: cols, Rows: [][]*string{}}
	for rows.Next() {
		if len(t.Rows) >= req.Limit {
			t.Truncated = true
			break
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make([]*string, len(cols))
		for i, v := range vals {
			row[i] = sqliteText(v)
		}
		t.Rows = append(t.Rows, row)
	}
	return t, rows.Err()
}

// sqliteText renders one SQLite value as text; nil is NULL, and a blob that
// is not UTF-8 is its hex literal.
func sqliteText(v any) *string {
	var s string
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		if utf8.Valid(x) {
			s = string(x)
		} else {
			if len(x) > dbMaxCellBytes/2 {
				x = x[:dbMaxCellBytes/2]
			}
			s = "x'" + hex.EncodeToString(x) + "'"
		}
	case time.Time:
		s = x.Format(time.RFC3339Nano)
	case string:
		s = x
	default:
		s = fmt.Sprint(x)
	}
	return &s
}
