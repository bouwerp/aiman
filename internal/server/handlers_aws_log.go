package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bouwerp/aiman/internal/domain"
)

type awsLogCall struct {
	SinceSeq int64 `json:"since_seq"`
	Limit    int   `json:"limit"`
}

// handleAWSLog reads the sign-in ledger, which lives under
// domain.AWSLoginLedger so no session's session.log reaches it. Only the
// operator may read it: a login's history says when host credentials changed.
func (s *Server) handleAWSLog(ctx context.Context, req Request) Response {
	if s.isSessionCall(ctx, req) {
		return errResp(req.ID, CodeNeedsOperator, "only the operator may read the AWS sign-in log")
	}
	var p awsLogCall
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, "invalid params")
		}
	}
	lister, ok := s.repo.(sessionEventLister)
	if !ok {
		return errResp(req.ID, CodeUnavailable, "aws: the ledger is not available")
	}
	events, err := lister.ListSessionEvents(ctx, domain.AWSLoginLedger, p.SinceSeq, p.Limit)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, fmt.Sprintf("listing aws sign-in events: %v", err))
	}
	if events == nil {
		events = []domain.SessionEvent{}
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type":   "aws_log",
		"events": events,
	}}
}
