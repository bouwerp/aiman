package server

import (
	"context"
	"time"

	"github.com/bouwerp/aiman/internal/harness/edge"
)

// HarnessAWSCallsTimeout bounds one harness.aws_calls read.
const HarnessAWSCallsTimeout = 15 * time.Second

// handleHarnessAWSCalls answers harness.aws_calls {session? | run?,
// after_id?, limit?}: the AWS API calls the run's edge tap recorded with an
// ID above after, oldest first, from the edge while the run is live and
// from aws_calls.json once it stopped. Serve redacts every call again
// before it leaves the host.
func (s *Server) handleHarnessAWSCalls(ctx context.Context, req Request, p harnessParams) Response {
	if p.After < 0 || p.Limit < 0 {
		return errResp(req.ID, CodeInvalidParams, "after_id and limit must not be negative")
	}
	run, fail := s.harnessRun(ctx, req, p, true)
	if fail != nil {
		return *fail
	}
	cctx, cancel := context.WithTimeout(ctx, HarnessAWSCallsTimeout)
	defer cancel()
	calls, info, err := s.harness.d.Manager.AWSCalls(cctx, run.ID, p.After, p.Limit)
	if err != nil {
		return harnessErr(req.ID, err, CodeUnavailable)
	}
	redacted := 0
	for i := range calls {
		calls[i].Redacted += edge.RedactAWSCall(&calls[i])
		redacted += calls[i].Redacted
	}
	service := info.Service
	if service == "" && len(run.AWSServices) > 0 {
		service = run.AWSServices[0]
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type": "harness_aws_calls", "run": run.ID, "service": service, "state": run.State,
		"calls": calls, "next": info.Next, "more": info.More, "total": info.Total, "dropped": info.Dropped,
		"redacted": redacted,
	}}
}
