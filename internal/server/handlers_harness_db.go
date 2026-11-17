package server

import (
	"context"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/harness"
)

// HarnessDBTimeout bounds one harness.db call: the read's own 5s statement
// timeout, the connection through the edge, or the SQLite reader container.
// It stays under the client's default 30s deadline.
const HarnessDBTimeout = 25 * time.Second

// handleHarnessDB answers the database browser. A session caller may read
// a run in its scope; a write is the operator's and is refused before the
// run is looked up.
func (s *Server) handleHarnessDB(ctx context.Context, req Request, p harnessParams) Response {
	session := s.isSessionCall(ctx, req)
	if p.Write && session {
		return errResp(req.ID, CodeNeedsOperator, "only the operator writes to a harness database")
	}
	if strings.TrimSpace(p.Service) == "" {
		return errResp(req.ID, CodeInvalidParams, "service is required")
	}
	run, fail := s.harnessRun(ctx, req, p, false)
	if fail != nil {
		return *fail
	}
	ctx, cancel := context.WithTimeout(ctx, HarnessDBTimeout)
	defer cancel()
	op := strings.TrimSpace(p.Op)
	if p.Write && op == "" {
		op = harness.DBOpQuery
	}
	res, err := s.harness.d.Manager.DB(ctx, harness.DBRequest{
		Run: run.ID, Service: strings.TrimSpace(p.Service), Op: op,
		Table: p.Table, SQL: p.SQL, Limit: p.Limit, Write: p.Write,
		Operator: !session, Actor: s.harnessActor(ctx, req),
	})
	if err != nil {
		return harnessErr(req.ID, err, CodeUnavailable)
	}
	return Response{ID: req.ID, Result: harnessDBReply{Type: "harness_db", DBResult: res}}
}

// harnessDBReply is the harness_db result: the type beside the result's
// fields.
type harnessDBReply struct {
	Type string `json:"type"`
	harness.DBResult
}
