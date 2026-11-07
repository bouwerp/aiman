package transcript

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	claudeCompactBoundary  = "compact_boundary"
	claudeNotificationKind = "task-notification"
	claudeNotificationTag  = "<" + claudeNotificationKind + ">"
)

// claudeLine is the part of a transcript line the normaliser reads.
type claudeLine struct {
	Type             string `json:"type"`
	Subtype          string `json:"subtype"`
	UUID             string `json:"uuid"`
	ParentUUID       string `json:"parentUuid"`
	Timestamp        string `json:"timestamp"`
	IsMeta           bool   `json:"isMeta"`
	IsCompactSummary bool   `json:"isCompactSummary"`
	Origin           struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
}

// claudeBlock is one message.content entry.
type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// claudeToolResult is a line's toolUseResult. Claude also writes it as a bare
// string, which decodes to the zero value.
type claudeToolResult struct {
	DurationSeconds  *float64 `json:"durationSeconds"`
	BackgroundTaskID string   `json:"backgroundTaskId"`
	AgentID          string   `json:"agentId"`
	Status           string   `json:"status"`
	OutputFile       string   `json:"outputFile"`
}

// claudeEvent is one line being applied, and what applying it produced.
type claudeEvent struct {
	claudeLine
	pos     int64
	at      time.Time
	text    string // string message.content
	blocks  []claudeBlock
	node    *claudeNode
	deltas  []Delta
	changed bool
}

func parseClaudeLine(pos int64, line []byte) (*claudeEvent, bool) {
	e := &claudeEvent{pos: pos}
	if json.Unmarshal(line, &e.claudeLine) != nil || e.UUID == "" {
		return nil, false
	}
	e.at, _ = time.Parse(time.RFC3339Nano, e.Timestamp)
	if e.Type == "user" || e.Type == "assistant" {
		e.text, e.blocks = decodeClaudeContent(e.Message.Content)
	}
	return e, true
}

func decodeClaudeContent(raw json.RawMessage) (string, []claudeBlock) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var blocks []claudeBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return "", nil
	}
	return "", blocks
}

// isNotification reports a task-notification. A line from before origin was
// written is told by its opening tag, so its summary cannot become user text.
func (e *claudeEvent) isNotification() bool {
	if e.Origin.Kind != "" {
		return e.Origin.Kind == claudeNotificationKind
	}
	return strings.HasPrefix(e.text, claudeNotificationTag)
}

// opensTurn reports a line that holds words a person typed, and nothing the
// agent's own loop wrote.
func (e *claudeEvent) opensTurn() bool {
	if e.Type != "user" || e.IsMeta || e.IsCompactSummary || e.isNotification() {
		return false
	}
	if e.text != "" {
		return true
	}
	hasText := false
	for i := range e.blocks {
		switch e.blocks[i].Type {
		case "tool_result":
			return false
		case "text":
			hasText = hasText || e.blocks[i].Text != ""
		}
	}
	return hasText
}

// plainText is the line's string content, or its text blocks joined.
func (e *claudeEvent) plainText() string {
	if e.text != "" {
		return e.text
	}
	var parts []string
	for i := range e.blocks {
		if e.blocks[i].Type == "text" {
			parts = append(parts, e.blocks[i].Text)
		}
	}
	return strings.Join(parts, "\n")
}

func (e *claudeEvent) toolUseResult() claudeToolResult {
	var r claudeToolResult
	if len(e.ToolUseResult) > 0 && json.Unmarshal(e.ToolUseResult, &r) != nil {
		return claudeToolResult{}
	}
	return r
}
