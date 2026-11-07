// Package transcript reads agent transcript files and pages them out as
// normalised items for the phone (docs/specs/phone-transcript.md).
package transcript

import "time"

// ItemKind is the kind of one normalised entry.
type ItemKind string

// Item kinds. The phone's ephemeral live item is not built by serve.
const (
	KindUser      ItemKind = "user"
	KindAssistant ItemKind = "assistant"
	KindThinking  ItemKind = "thinking"
	KindToolCall  ItemKind = "tool_call"
	KindSubagent  ItemKind = "subagent"
	KindProcess   ItemKind = "process"
	KindSystem    ItemKind = "system"
	KindPane      ItemKind = "pane"
)

// Page sources.
const (
	SourceTranscript = "transcript"
	SourcePane       = "pane"
)

// PageType is Page.Type on every page.
const PageType = "transcript"

// Capabilities.Scrollback values.
const (
	ScrollbackTranscript = "transcript"
	ScrollbackPane       = "pane"
)

// Capabilities.Thinking values.
const (
	ThinkingFull    = "full"
	ThinkingSummary = "summary"
	ThinkingNone    = "none"
)

// Capabilities.Processes values.
const (
	ProcessesFile   = "file"
	ProcessesInline = "inline"
	ProcessesNone   = "none"
)

// ToolCall.Status values.
const (
	ToolRunning = "running"
	ToolOK      = "ok"
	ToolError   = "error"
	ToolDenied  = "denied"
)

// Subagent.State values.
const (
	SubagentRunning = "running"
	SubagentDone    = "done"
	SubagentFailed  = "failed"
	SubagentUnknown = "unknown"
)

// Process.State values.
const (
	ProcessRunning = "running"
	ProcessExited  = "exited"
	ProcessUnknown = "unknown"
)

// Process.Output values.
const (
	OutputFile   = "file"
	OutputInline = "inline"
)

// Item is one normalised transcript entry. Rev is the position of the last
// line that changed it; Text is capped by IndexOptions.MaxFieldBytes.
type Item struct {
	ID        string    `json:"id"`
	Kind      ItemKind  `json:"kind"`
	At        time.Time `json:"at"`
	Rev       int64     `json:"rev"`
	Text      string    `json:"text"`
	Truncated bool      `json:"truncated"`
	Hidden    bool      `json:"hidden,omitempty"`
	ToolCall  *ToolCall `json:"tool_call,omitempty"`
	Subagent  *Subagent `json:"subagent,omitempty"`
	Process   *Process  `json:"process,omitempty"`
}

// ToolCall is a tool invocation paired with its result.
type ToolCall struct {
	ToolID     string `json:"tool_id"`
	Name       string `json:"name"`
	Summary    string `json:"summary"`
	Input      string `json:"input"`
	Result     string `json:"result"`
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	SubagentID string `json:"subagent_id,omitempty"`
	ProcessID  string `json:"process_id,omitempty"`
}

// Subagent is a child conversation inside the agent's own process.
type Subagent struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	State       string     `json:"state"`
	ItemCount   int        `json:"item_count"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
}

// Process is a background shell command the agent started.
type Process struct {
	ID       string `json:"id"`
	Command  string `json:"command"`
	State    string `json:"state"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Output   string `json:"output"`
}

// Capabilities says what one agent's transcript can show.
type Capabilities struct {
	Agent       string `json:"agent"`
	Scrollback  string `json:"scrollback"`
	ToolResults bool   `json:"tool_results"`
	Thinking    string `json:"thinking"`
	Subagents   bool   `json:"subagents"`
	Processes   string `json:"processes"`
}
