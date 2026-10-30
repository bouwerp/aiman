package server

import (
	"encoding/json"
	"fmt"
)

const ProtocolVersion = 1

const (
	CodeInvalidParams    = "invalid_params"
	CodeNotFound         = "not_found"
	CodeNameTaken        = "name_taken"
	CodeServerNotRunning = "server_not_running"
	CodeAlreadyRunning   = "already_running"
	CodeAgentBlocked     = "agent_blocked"
	CodeTimeout          = "timeout"
	CodeCreateFailed     = "create_failed"
	CodePublishFailed    = "publish_failed"
	CodeJiraUnavailable  = "jira_unavailable"
	CodeProtocolMismatch = "protocol_mismatch"
	CodeNoReviewer       = "no_reviewer_agent"
	CodeNoPreferredAgent = "no_preferred_agent"
	// CodeLegacyTerminal refuses a session that still runs in tmux. The
	// message carries the command that stops it on the host.
	CodeLegacyTerminal = "legacy_terminal"
	// CodeGateNeedsOperator refuses a session's attempt to close, drop, remove
	// or edit the open design gate, or to approve a design for another session.
	CodeGateNeedsOperator = "gate_needs_operator"
	// CodeDesignNotApproved refuses delegation while the parent's design gate is open.
	CodeDesignNotApproved = "design_not_approved"
	// CodeConsultNeedsSmart refuses a consult when the smart tier has no
	// installed agent; a consult never falls back to the worker tier.
	CodeConsultNeedsSmart = "consult_needs_smart"
	// CodeReviewExists refuses a review create while a live session already
	// reviews that pull request.
	CodeReviewExists = "review_exists"
	// CodeReviewNotReady refuses a reveal before the blind passes are done,
	// and a post before the description has been revealed.
	CodeReviewNotReady = "review_not_ready"
	// CodeReviewIncomplete refuses a post while the review file lacks the
	// operator's answers or a call on every finding.
	CodeReviewIncomplete = "review_incomplete"
	// CodeReviewHeadMoved refuses a post when the PR head is no longer the
	// one the review was staged from.
	CodeReviewHeadMoved = "review_head_moved"
	// CodeReviewAlreadyPosted refuses a second post until a refresh reopens
	// the Post the review to-do.
	CodeReviewAlreadyPosted = "review_already_posted"
	// CodeReviewNoCoverage refuses review.coverage when the tree has no
	// go.mod and the repository has no review.coverage command.
	CodeReviewNoCoverage = "review_no_coverage"
)

type Request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Caller string          `json:"caller,omitempty"`
}

type Response struct {
	ID     string `json:"id"`
	Result any    `json:"result,omitempty"`
	Error  *Error `json:"error,omitempty"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func EncodeResponse(resp Response) ([]byte, error) {
	b, err := json.Marshal(resp)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
