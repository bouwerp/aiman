package domain

import (
	"encoding/json"
	"fmt"
	"time"
)

// FeedSource names where a feed event came from.
type FeedSource string

// FeedEventClass is how the collector classed an event.
type FeedEventClass string

// FeedEvent is one collected event in the operator's feed.
type FeedEvent struct {
	ID         int64          `json:"id"`
	Source     FeedSource     `json:"source"`
	ExternalID string         `json:"external_id"` // slack: "<channel>/<ts>"; github: notification id; aiman: "notify:<seq>" or "ledger:<rowid>"
	At         time.Time      `json:"at"`
	Actor      string         `json:"actor"` // slack user id, github login, session id
	ActorName  string         `json:"actor_name,omitempty"`
	Kind       string         `json:"kind"` // slack: "message"; github: notification reason; aiman: notify kind or ledger kind
	Title      string         `json:"title"`
	Body       string         `json:"body"` // redacted, at most 4 KiB
	URL        string         `json:"url,omitempty"`
	ThreadKey  string         `json:"thread_key"`
	Class      FeedEventClass `json:"class"`
	Urgent     bool           `json:"urgent"`
	Own        bool           `json:"own,omitempty"`        // written by the operator
	Enrichment string         `json:"enrichment,omitempty"` // redacted context text, at most 8 KiB
	Channel    string         `json:"channel,omitempty"`    // slack channel id, for ambient grouping
}

// FeedBatchState is the lifecycle of a batch: running, done or failed.
type FeedBatchState string

// FeedBatch is one cut of events handed to the agent job.
type FeedBatch struct {
	ID          int64          `json:"id"`
	EventIDs    []int64        `json:"event_ids"`
	UpToEventID int64          `json:"up_to_event_id"`
	State       FeedBatchState `json:"state"`
	Attempts    int            `json:"attempts"`
	Agent       string         `json:"agent,omitempty"`
	Model       string         `json:"model,omitempty"`
	Summary     string         `json:"summary,omitempty"`
	Error       string         `json:"error,omitempty"`
	StartedAt   time.Time      `json:"started_at"`
	EndedAt     time.Time      `json:"ended_at,omitempty"`
}

// FeedActionKind is what an action does when approved.
type FeedActionKind string

const (
	FeedSlackDraft   FeedActionKind = "slack_draft"
	FeedTodoAdd      FeedActionKind = "todo_add"
	FeedRead         FeedActionKind = "read"
	FeedSteerSession FeedActionKind = "steer_session" // phase 2
	FeedReviewPR     FeedActionKind = "review_pr"     // phase 2
	FeedSlackReply   FeedActionKind = "slack_reply"   // phase 2
	FeedStartSession FeedActionKind = "start_session" // phase 2
)

// FeedPhase1Kinds are the kinds phase 1 executes; they are also the safe kinds
// a paired device may approve.
func FeedPhase1Kinds() []FeedActionKind {
	return []FeedActionKind{FeedSlackDraft, FeedTodoAdd, FeedRead}
}

// FeedSafeKind reports whether k is one of FeedPhase1Kinds.
func FeedSafeKind(k FeedActionKind) bool {
	for _, p := range FeedPhase1Kinds() {
		if k == p {
			return true
		}
	}
	return false
}

// FeedOption is one choice an action offers.
type FeedOption struct {
	Label  string          `json:"label"`
	Kind   FeedActionKind  `json:"kind,omitempty"` // empty: the action's kind
	Params json.RawMessage `json:"params"`
	Why    string          `json:"why,omitempty"`
}

// EffectiveKind is o.Kind, else the action's kind.
func (o FeedOption) EffectiveKind(action FeedActionKind) FeedActionKind {
	if o.Kind != "" {
		return o.Kind
	}
	return action
}

// FeedTraceKind is the type of a trace entry: event, context, memory, decision or batch.
type FeedTraceKind string

// FeedTraceRole is why a trace entry is cited: answers, cause or context.
type FeedTraceRole string

// FeedTraceEntry is one cited source behind an action.
type FeedTraceEntry struct {
	Kind    FeedTraceKind `json:"kind"`
	Ref     string        `json:"ref"` // event id, memory bullet id, action id, batch id, as text
	At      time.Time     `json:"at"`
	Role    FeedTraceRole `json:"role"`
	Excerpt string        `json:"excerpt"`
	URL     string        `json:"url,omitempty"`
}

// FeedActionState is proposed, approved, running, done, failed, dismissed,
// expired or snoozed.
type FeedActionState string

// FeedPriority is high, normal or low.
type FeedPriority string

// FeedDismissReason is why the operator dismissed an action.
type FeedDismissReason string

const (
	FeedNotRelevant FeedDismissReason = "not_relevant"
	FeedHandled     FeedDismissReason = "handled"
	FeedLater       FeedDismissReason = "later"
	FeedMuteThread  FeedDismissReason = "mute_thread"
	FeedMuteSender  FeedDismissReason = "mute_sender"
	FeedMuteChannel FeedDismissReason = "mute_channel" // only on an action whose answered event is a channel digest
)

// FeedDismissal records how an action was dismissed or snoozed.
type FeedDismissal struct {
	Reason FeedDismissReason `json:"reason"`
	Note   string            `json:"note,omitempty"`
	Until  time.Time         `json:"until,omitempty"` // later
}

// FeedAction is something the agent job proposes for the operator to decide.
type FeedAction struct {
	ID           int64            `json:"id"`
	BatchID      int64            `json:"batch_id"`
	Kind         FeedActionKind   `json:"kind"`
	Rationale    string           `json:"rationale"`
	Priority     FeedPriority     `json:"priority"`
	Answers      []int64          `json:"answers"`
	Options      []FeedOption     `json:"options"` // 1 to 4
	Trace        []FeedTraceEntry `json:"trace"`
	State        FeedActionState  `json:"state"`
	Chosen       *int             `json:"chosen,omitempty"`
	ChosenParams json.RawMessage  `json:"chosen_params,omitempty"`
	Result       string           `json:"result,omitempty"`
	DecidedBy    string           `json:"decided_by,omitempty"`
	Dismissal    *FeedDismissal   `json:"dismissal,omitempty"`
	CreatedAt    time.Time        `json:"created_at"`
	DecidedAt    time.Time        `json:"decided_at,omitempty"`
	ExpiresAt    time.Time        `json:"expires_at"`
}

// FeedMuteScope is thread, sender or channel.
type FeedMuteScope string

// FeedMute silences a thread, sender or channel until Until.
type FeedMute struct {
	Scope    FeedMuteScope `json:"scope"`
	Key      string        `json:"key"` // thread key, actor id, channel id
	Until    time.Time     `json:"until"`
	ActionID int64         `json:"action_id"`
}

// FeedThreadKeySlack is the thread key of a Slack message: its thread's
// timestamp, else its own.
func FeedThreadKeySlack(channel, threadTS, ts string) string {
	if threadTS == "" {
		threadTS = ts
	}
	return fmt.Sprintf("slack:%s/%s", channel, threadTS)
}

// FeedThreadKeyGitHub is the thread key of a GitHub notification: the pull
// request or issue when known, else the notification id.
func FeedThreadKeyGitHub(repo string, number int, notificationID string) string {
	if repo == "" || number == 0 {
		return "gh:" + notificationID
	}
	return fmt.Sprintf("gh:%s#%d", repo, number)
}

// FeedThreadKeySession is the thread key of an aiman session's events.
func FeedThreadKeySession(id string) string { return "aiman:" + id }

// FeedThreadKeyDrive is the thread key of a Drive file.
func FeedThreadKeyDrive(fileID string) string { return "drive:" + fileID }
