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
