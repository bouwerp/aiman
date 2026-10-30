package domain

import (
	"encoding/json"
	"time"
)

// EventKind is one lifecycle fact in the session ledger. The dotted names are
// wire contracts: `aiman session log` and the eval suite read them.
type EventKind string

const (
	EventSessionCreated  EventKind = "session.created"
	EventPromptQueued    EventKind = "prompt.queued"
	EventPromptSubmitted EventKind = "prompt.submitted"
	EventHookReported    EventKind = "hook.reported"
	EventTodoChanged     EventKind = "todo.changed"
	EventSteerQueued     EventKind = "steer.queued"
	EventSteerDelivered  EventKind = "steer.delivered"
	EventHandoverWritten EventKind = "handover.written"
	EventVerifyRecorded  EventKind = "verify.recorded"
	EventUsageBudget     EventKind = "usage.budget"
	EventSessionEnded    EventKind = "session.ended"
	// EventHandoverChildStarted and EventHandoverChildFinished bracket one
	// handover child run on the worker's ledger.
	EventHandoverChildStarted  EventKind = "handover.child_started"
	EventHandoverChildFinished EventKind = "handover.child_finished"
	// EventPaneUncertain records a pane the rules could not classify with high
	// confidence. It measures how often a model tier would be needed.
	EventPaneUncertain EventKind = "pane.uncertain"
	// EventDesignApproved records the operator's approval that closed a
	// session's design gate; its payload carries the approving quote.
	EventDesignApproved EventKind = "design.approved"
	// EventReviewApproved records the operator's approval that closed a
	// session's review gate; its payload carries the approving quote.
	EventReviewApproved EventKind = "review.approved"
	// EventDesignPublished records a design page push: revision, url and the
	// Decisions-table triage counts.
	EventDesignPublished EventKind = "design.published"
	// EventConsultFinished records a consult's report on its parent's ledger:
	// objection counts by severity and the verdict.
	EventConsultFinished EventKind = "consult.finished"
	// EventReviewStarted records a review session's start: the PR, its base
	// and the head SHA the worktree was staged from.
	EventReviewStarted EventKind = "review.started"
	// EventReviewRevealed records that the PR description was written for a
	// review session; single is set when the review went ahead with one
	// reviewer.
	EventReviewRevealed EventKind = "review.revealed"
	// EventReviewRefreshed records a review restaged at a new PR head: from
	// and to are the old and new head SHAs.
	EventReviewRefreshed EventKind = "review.refreshed"
	// EventConsultAbandoned records on the parent's ledger a consult that
	// ended without a usable report: the child and the to-do it was for.
	EventConsultAbandoned EventKind = "consult.abandoned"
)

// EventActor is who caused the event. `unknown` exists for rows imported from a
// source that did not record an author; writers should not use it.
type EventActor string

const (
	EventActorUser     EventActor = "user"
	EventActorAgent    EventActor = "agent"
	EventActorAiman    EventActor = "aiman"
	EventActorVerifier EventActor = "verifier"
	EventActorUnknown  EventActor = "unknown"
)

// SessionEvent is one append-only ledger entry. Seq is assigned per session and
// starts at 1. Payload is the event-specific JSON. Hash is sha256(Payload), so a
// replay can detect a tampered or truncated row.
type SessionEvent struct {
	SessionID string          `json:"session_id"`
	Seq       int64           `json:"seq"`
	At        time.Time       `json:"at"`
	Actor     EventActor      `json:"actor"`
	Kind      EventKind       `json:"kind"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Hash      string          `json:"hash,omitempty"`
}
