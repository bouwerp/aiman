package harness

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// PGConn is where a postgres read connects: the edge relay's host port, as
// aiman_ro with the run's password.
type PGConn struct {
	Host     string
	Port     int
	User     string
	Password string
	Database string
}

// PostgresReader runs one read statement and returns at most limit rows,
// marking the table truncated when more existed.
type PostgresReader interface {
	Read(ctx context.Context, c PGConn, sql string, limit int) (*DBTable, error)
}

// PGReader reads over the simple query protocol with pgconn: one statement
// inside BEGIN READ ONLY, with the session itself read-only and under
// statement_timeout, then ROLLBACK.
type PGReader struct{}

// pgConnectTimeout bounds the connection through the relay.
const pgConnectTimeout = 5 * time.Second

// pgReadParams are the session parameters of every read connection: no
// transaction may write, and no statement runs past DBStatementTimeout.
func pgReadParams() map[string]string {
	return map[string]string{
		"application_name":              "aiman-harness-db",
		"default_transaction_read_only": "on",
		"statement_timeout":             strconv.Itoa(int(DBStatementTimeout / time.Millisecond)),
	}
}

// pgReadPlan is the statements a read sends, in order. The middle one is the
// checked statement; the transaction around it is read only and never
// commits.
func pgReadPlan(sql string) []string {
	return []string{"BEGIN READ ONLY", sql, "ROLLBACK"}
}

// pgReadConfig builds the connection config from c alone. The environment
// (PGHOST, PGSSLMODE, a .pgpass file) does not choose the target, the user
// or the password.
func pgReadConfig(c PGConn) (*pgconn.Config, error) {
	cfg, err := pgconn.ParseConfig("sslmode=disable")
	if err != nil {
		return nil, err
	}
	cfg.Host, cfg.Port = c.Host, uint16(c.Port) //nolint:gosec // G115: a TCP port Docker published
	cfg.User, cfg.Password, cfg.Database = c.User, c.Password, c.Database
	cfg.ConnectTimeout = pgConnectTimeout
	cfg.TLSConfig, cfg.Fallbacks = nil, nil
	cfg.KerberosSrvName, cfg.KerberosSpn = "", ""
	cfg.RuntimeParams = pgReadParams()
	return cfg, nil
}

// Read connects, runs the plan and closes the connection.
func (PGReader) Read(ctx context.Context, c PGConn, sql string, limit int) (*DBTable, error) {
	cfg, err := pgReadConfig(c)
	if err != nil {
		return nil, err
	}
	conn, err := pgconn.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres as %s through the edge: %w", c.User, err)
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = conn.Close(cctx)
	}()
	plan := pgReadPlan(sql)
	if err := pgExec(ctx, conn, plan[0]); err != nil {
		return nil, err
	}
	t, err := pgQuery(ctx, conn, plan[1], limit)
	if err != nil {
		return nil, err
	}
	_ = pgExec(ctx, conn, plan[2])
	return t, nil
}

func pgExec(ctx context.Context, conn *pgconn.PgConn, sql string) error {
	_, err := conn.Exec(ctx, sql).ReadAll()
	return pgErr(err)
}

// pgQuery reads the first result of sql up to limit rows; one more row
// marks it truncated.
func pgQuery(ctx context.Context, conn *pgconn.PgConn, sql string, limit int) (*DBTable, error) {
	mrr := conn.Exec(ctx, sql)
	t := &DBTable{Columns: []string{}, Rows: [][]*string{}}
	if mrr.NextResult() {
		rr := mrr.ResultReader()
		for _, fd := range rr.FieldDescriptions() {
			t.Columns = append(t.Columns, fd.Name)
		}
		for rr.NextRow() {
			if len(t.Rows) >= limit {
				t.Truncated = true
				break
			}
			vals := rr.Values()
			row := make([]*string, len(vals))
			for i, v := range vals {
				if v != nil {
					s := string(v)
					row[i] = &s
				}
			}
			t.Rows = append(t.Rows, row)
		}
		tag, err := rr.Close()
		if err != nil {
			_ = mrr.Close()
			return nil, pgErr(err)
		}
		if len(t.Columns) == 0 {
			t.Tag = tag.String()
		}
	}
	if err := mrr.Close(); err != nil {
		return nil, pgErr(err)
	}
	return t, nil
}

// pgErr turns the server's refusal into the caller's mistake; a broken
// connection stays an error of its own.
func pgErr(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return dbQueryError(pe.Severity + ": " + pe.Message + " (SQLSTATE " + pe.Code + ")")
	}
	return err
}
