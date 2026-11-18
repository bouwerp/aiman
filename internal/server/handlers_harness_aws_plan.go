package server

import (
	"context"

	"github.com/bouwerp/aiman/internal/harness"
)

// handleHarnessAWSPlan answers harness.aws_plan {session?, scenario?}: the
// plan of the scenario's aws service (or of every aws service, without
// scenario overrides) read from the session's commit, with no run. Any
// in-scope caller may ask; it only reads the tree.
func (s *Server) handleHarnessAWSPlan(ctx context.Context, req Request, p harnessParams) Response {
	sess, fail := s.harnessSession(ctx, req, p.Session)
	if fail != nil {
		return *fail
	}
	src, err := s.harnessSourceFor(ctx, sess)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	sha, plans, err := s.harness.d.Manager.AWSPlans(ctx, harness.AWSPlanRequest{
		Repo: src.repo, SHA: src.sha, Override: src.override, Scenario: p.Scenario,
	})
	if err != nil {
		return harnessErr(req.ID, err, CodeInvalidParams)
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "harness_aws_plan", "session": sess.ID, "sha": sha, "plans": plans}}
}
