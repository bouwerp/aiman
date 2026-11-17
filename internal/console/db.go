package console

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/bouwerp/aiman/internal/harness"
)

// Database tab paths: GET lists tables or reads a table's rows, POST runs a
// statement (a read, or the operator's write).
const (
	DBPath      = "/api/db"
	DBQueryPath = "/api/db/query"
)

func init() {
	apiRoutes[DBPath] = apiRoute{http.MethodGet, (*Server).handleDB}
	apiRoutes[DBQueryPath] = apiRoute{http.MethodPost, (*Server).handleDBQuery}
	staticFiles["/db.js"] = [2]string{"web/db.js", "text/javascript; charset=utf-8"}
	staticFiles["/db.css"] = [2]string{"web/db.css", "text/css; charset=utf-8"}
}

// handleDB answers GET /api/db?run&service&op=tables|rows&table&limit.
func (s *Server) handleDB(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	service, op := strings.TrimSpace(q.Get("service")), q.Get("op")
	if service == "" {
		badParams(w, "name a database service")
		return
	}
	params := map[string]any{"service": service, "op": op}
	switch op {
	case harness.DBOpTables:
	case harness.DBOpRows:
		table := strings.TrimSpace(q.Get("table"))
		if table == "" {
			badParams(w, "name a table")
			return
		}
		params["table"] = table
	default:
		badParams(w, "op is tables or rows; a statement goes to "+DBQueryPath)
		return
	}
	if l := q.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n <= 0 {
			badParams(w, "limit must be a positive number")
			return
		}
		params["limit"] = n
	}
	if !s.sameRun(w, q.Get("run")) {
		return
	}
	params["run"] = s.currentRun()
	s.relay(w, r, "harness.db", params)
}

// handleDBQuery answers POST /api/db/query {run, service, sql, limit?,
// write?}. The console calls serve as the operator, so serve accepts a
// write from here; the page asks the tester to confirm it first.
func (s *Server) handleDBQuery(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Run     string `json:"run"`
		Service string `json:"service"`
		SQL     string `json:"sql"`
		Limit   int    `json:"limit"`
		Write   bool   `json:"write"`
	}
	if !decodeBody(w, r, &b) {
		return
	}
	if strings.TrimSpace(b.Service) == "" || strings.TrimSpace(b.SQL) == "" {
		badParams(w, "name a database service and a statement")
		return
	}
	if b.Limit < 0 {
		badParams(w, "limit must be a positive number")
		return
	}
	if !s.sameRun(w, b.Run) {
		return
	}
	params := map[string]any{"run": b.Run, "service": strings.TrimSpace(b.Service), "op": harness.DBOpQuery, "sql": b.SQL}
	if b.Limit > 0 {
		params["limit"] = b.Limit
	}
	if b.Write {
		params["write"] = true
	}
	s.relay(w, r, "harness.db", params)
}
