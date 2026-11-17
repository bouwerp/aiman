package server

import (
	"context"
	"time"

	"github.com/bouwerp/aiman/internal/harness/edge"
)

// HarnessCaptureTimeout bounds one harness.capture read.
const HarnessCaptureTimeout = 15 * time.Second

// handleHarnessCapture answers harness.capture {session? | run?, after_id?,
// limit?}: the flows the run's edge proxy recorded with an ID above after,
// oldest first, from the edge while the run is live and from capture.json
// once it stopped. Serve redacts every flow again before it leaves the host.
func (s *Server) handleHarnessCapture(ctx context.Context, req Request, p harnessParams) Response {
	if p.After < 0 || p.Limit < 0 {
		return errResp(req.ID, CodeInvalidParams, "after_id and limit must not be negative")
	}
	run, fail := s.harnessRun(ctx, req, p, true)
	if fail != nil {
		return *fail
	}
	cctx, cancel := context.WithTimeout(ctx, HarnessCaptureTimeout)
	defer cancel()
	flows, info, err := s.harness.d.Manager.Capture(cctx, run.ID, p.After, p.Limit)
	if err != nil {
		return harnessErr(req.ID, err, CodeUnavailable)
	}
	redacted := 0
	for i := range flows {
		redacted += edge.RedactFlow(&flows[i])
	}
	network := info.Network
	if network == "" {
		network = run.Network
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type": "harness_capture", "run": run.ID, "network": network, "state": run.State,
		"flows": flows, "next": info.Next, "more": info.More, "total": info.Total, "dropped": info.Dropped,
		"redacted": redacted,
	}}
}
