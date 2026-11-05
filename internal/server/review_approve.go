package server

import "context"

// handleReviewAssess starts the consult whose verdict decides whether a PR may
// be approved.
func (s *Server) handleReviewAssess(_ context.Context, req Request) Response {
	return errResp(req.ID, CodeInvalidParams, "review.assess is not available yet")
}

// handleReviewApprove submits APPROVE once the assessment and the operator's
// sign-off line up with the PR's head.
func (s *Server) handleReviewApprove(_ context.Context, req Request) Response {
	return errResp(req.ID, CodeInvalidParams, "review.approve is not available yet")
}
