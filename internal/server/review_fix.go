package server

import "context"

// handleReviewFix starts a fixer for a review's findings.
func (s *Server) handleReviewFix(_ context.Context, req Request) Response {
	return errResp(req.ID, CodeInvalidParams, "review.fix is not available yet")
}

// handleReviewPush checks a fixer's work and pushes it to the PR's branch.
func (s *Server) handleReviewPush(_ context.Context, req Request) Response {
	return errResp(req.ID, CodeInvalidParams, "review.push is not available yet")
}
