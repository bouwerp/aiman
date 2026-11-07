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
	// CodeWorktreeDirty refuses session.terminate while removing the worktree
	// would lose work. Error.Data is a WorktreeDirtyData.
	CodeWorktreeDirty = "worktree_dirty"
	// CodeNeedsOperator refuses a session's call to a method only the operator
	// may use, such as terminating or restarting a session.
	CodeNeedsOperator = "needs_operator"
	// CodeSessionScope refuses a session's call that targets a session other
	// than itself or one of its descendants.
	CodeSessionScope = "session_scope"
	// CodeUnavailable means serve could not get what was asked for and has
	// nothing stale to offer. The gateway returns it too.
	CodeUnavailable = "unavailable"
	// CodeAWSNoSSO refuses aws.login_start for a delegation with no SSO facts.
	CodeAWSNoSSO = "aws_no_sso"
	// CodeAWSRateLimited refuses aws.login_start over the pending-login or
	// start-rate limit.
	CodeAWSRateLimited = "aws_rate_limited"
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
	// Data is the refusal's structured detail, for the codes that have one.
	Data any `json:"data,omitempty"`
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
