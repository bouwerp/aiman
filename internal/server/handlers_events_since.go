package server

import (
	"context"
	"encoding/json"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
)

const (
	eventsSinceDefaultLimit = 200
	eventsSinceMaxLimit     = 1000
)

// ledgerReader is the cross-session ledger read surface of the sqlite
// repository.
type ledgerReader interface {
	ScanEventsAfterRowID(ctx context.Context, afterRowID int64, kinds []domain.EventKind, limit int) ([]sqlite.LedgerRow, int64, error)
	LastEventRowID(ctx context.Context) (int64, error)
}

type eventsSinceEvent struct {
	RowID     int64             `json:"rowid"`
	SessionID string            `json:"session_id"`
	Seq       int64             `json:"seq"`
	At        string            `json:"at"`
	Actor     domain.EventActor `json:"actor"`
	Kind      domain.EventKind  `json:"kind"`
	Payload   json.RawMessage   `json:"payload"`
}

// handleEventsSince answers session.events_since: ledger rows across sessions
// in rowid order. after_rowid -1 answers no rows and the tail as next.
func (s *Server) handleEventsSince(ctx context.Context, req Request) Response {
	if s.isSessionCall(ctx, req) {
		return errResp(req.ID, CodeNeedsOperator, "only the operator may read the ledger across sessions")
	}
	var params struct {
		AfterRowID int64    `json:"after_rowid"`
		Kinds      []string `json:"kinds"`
		Limit      int      `json:"limit"`
	}
	if !decodeOptional(req.Params, &params) {
		return errResp(req.ID, CodeInvalidParams, "invalid params")
	}
	reader, ok := s.repo.(ledgerReader)
	if !ok {
		return errResp(req.ID, CodeUnavailable, "the event ledger is not available")
	}
	limit := params.Limit
	if limit <= 0 {
		limit = eventsSinceDefaultLimit
	}
	if limit > eventsSinceMaxLimit {
		limit = eventsSinceMaxLimit
	}
	events := []eventsSinceEvent{}
	next := params.AfterRowID
	if params.AfterRowID < 0 {
		last, err := reader.LastEventRowID(ctx)
		if err != nil {
			return errResp(req.ID, CodeUnavailable, err.Error())
		}
		return Response{ID: req.ID, Result: map[string]any{"type": "session_events_since", "events": events, "next": last}}
	}
	kinds := make([]domain.EventKind, 0, len(params.Kinds))
	for _, k := range params.Kinds {
		kinds = append(kinds, domain.EventKind(k))
	}
	rows, scanned, err := reader.ScanEventsAfterRowID(ctx, params.AfterRowID, kinds, limit)
	if err != nil {
		return errResp(req.ID, CodeUnavailable, err.Error())
	}
	for _, row := range rows {
		payload := row.Payload
		if len(payload) == 0 {
			payload = json.RawMessage(`{}`)
		}
		events = append(events, eventsSinceEvent{
			RowID: row.RowID, SessionID: row.SessionID, Seq: row.Seq,
			At: row.At.Format(time.RFC3339Nano), Actor: row.Actor, Kind: row.Kind, Payload: payload,
		})
	}
	next = scanned
	return Response{ID: req.ID, Result: map[string]any{"type": "session_events_since", "events": events, "next": next}}
}
