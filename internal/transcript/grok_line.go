package transcript

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// grokLine is one parsed session update. Only the fields items show are
// decoded; everything else on the line, _meta included, is never read into
// an item.
type grokLine struct {
	pos  int64
	at   time.Time
	meta grokMeta
	upd  grokUpdate
}

type grokMeta struct {
	EventID          string `json:"eventId"`
	PromptID         string `json:"promptId"`
	AgentTimestampMs int64  `json:"agentTimestampMs"`
}

// grokUpdate is the union of the update shapes the normaliser reads.
type grokUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`

	// Chunks carry an object; tool_call_update carries a list.
	Content json.RawMessage `json:"content"`

	ToolCallID string          `json:"toolCallId"`
	Title      string          `json:"title"`
	Status     json.RawMessage `json:"status"`
	RawInput   json.RawMessage `json:"rawInput"`
	RawOutput  json.RawMessage `json:"rawOutput"`
	ToolMeta   struct {
		Tool struct {
			Name string `json:"name"`
		} `json:"x.ai/tool"`
	} `json:"_meta"`

	SubagentID     string  `json:"subagent_id"`
	ChildSessionID string  `json:"child_session_id"`
	SubagentType   string  `json:"subagent_type"`
	Description    string  `json:"description"`
	DurationMS     float64 `json:"duration_ms"`

	TaskID          string        `json:"task_id"`
	ToolCallIDSnake string        `json:"tool_call_id"`
	Command         string        `json:"command"`
	OutputFile      string        `json:"output_file"`
	TaskSnapshot    *grokSnapshot `json:"task_snapshot"`

	EventName string    `json:"event_name"`
	Runs      []grokRun `json:"runs"`

	Percentage    float64 `json:"percentage"`
	ContextWindow float64 `json:"context_window"`
	TokensBefore  float64 `json:"tokens_before"`
	TokensAfter   float64 `json:"tokens_after"`
}

type grokSnapshot struct {
	TaskID     string   `json:"task_id"`
	Command    string   `json:"command"`
	OutputFile string   `json:"output_file"`
	ExitCode   *float64 `json:"exit_code"`
}

type grokRun struct {
	Name   string `json:"name"`
	Status struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	} `json:"status"`
}

// parseGrokLine decodes a session/update or _x.ai/session/update line.
func parseGrokLine(pos int64, line []byte) (*grokLine, bool) {
	var env struct {
		Method    string  `json:"method"`
		Timestamp float64 `json:"timestamp"`
		Params    struct {
			Update json.RawMessage `json:"update"`
			Meta   grokMeta        `json:"_meta"`
		} `json:"params"`
	}
	if json.Unmarshal(line, &env) != nil {
		return nil, false
	}
	if env.Method != "session/update" && env.Method != "_x.ai/session/update" {
		return nil, false
	}
	l := &grokLine{pos: pos, meta: env.Params.Meta}
	if json.Unmarshal(env.Params.Update, &l.upd) != nil || l.upd.SessionUpdate == "" {
		return nil, false
	}
	l.at = grokTime(env.Params.Meta.AgentTimestampMs, env.Timestamp)
	return l, true
}

// key identifies the line: its eventId, else its position.
func (l *grokLine) key() string {
	if l.meta.EventID != "" {
		return l.meta.EventID
	}
	return strconv.FormatInt(l.pos, 10)
}

// chunkText is a chunk's text content.
func (u *grokUpdate) chunkText() string {
	var c struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(u.Content, &c) != nil {
		return ""
	}
	return c.Text
}

// contentText joins the text blocks of a tool_call_update's content list.
func (u *grokUpdate) contentText() string {
	var blocks []struct {
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(u.Content, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Content.Text != "" {
			parts = append(parts, b.Content.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// status is the update's status when it is a string, else "".
func (u *grokUpdate) status() string {
	var s string
	if json.Unmarshal(u.Status, &s) != nil {
		return ""
	}
	return s
}

// rawJSON returns a JSON value as text, or "" for an absent or null one.
func rawJSON(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	return s
}
