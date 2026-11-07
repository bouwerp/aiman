package transcript

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

const claudeSummaryBytes = 120

// claudeOutputPathRe finds the output file in a background result's text, for
// a line whose toolUseResult does not carry outputFile.
var claudeOutputPathRe = regexp.MustCompile(`Output is being written to: (\S+?\.output)`)

// claudeSummaryKeys are the input fields that describe a call, best first.
var claudeSummaryKeys = []string{"description", "file_path", "path", "pattern", "query", "url", "command", "prompt", "skill"}

func (c *claude) toolUse(e *claudeEvent, b *claudeBlock) {
	input := string(b.Input)
	it := c.addItem(e, Item{Kind: KindToolCall, ToolCall: &ToolCall{
		ToolID:  b.ID,
		Name:    b.Name,
		Summary: claudeToolSummary(b.Input),
		Input:   input,
		Status:  ToolRunning,
	}})
	c.calls[b.ID] = it
}

func claudeToolSummary(input json.RawMessage) string {
	var fields map[string]any
	if json.Unmarshal(input, &fields) != nil {
		return ""
	}
	for _, key := range claudeSummaryKeys {
		if s, ok := fields[key].(string); ok && s != "" {
			first, _, _ := strings.Cut(s, "\n")
			short, _ := Truncate(first, claudeSummaryBytes)
			return short
		}
	}
	return ""
}

// toolResult updates the call the result names and stops tracking it, since
// only a process or sub-agent can change it later. A result for a call never
// seen, already resolved, or on an abandoned branch changes nothing.
func (c *claude) toolResult(e *claudeEvent, b *claudeBlock) {
	it, ok := c.calls[b.ToolUseID]
	if !ok {
		return
	}
	res := e.toolUseResult()
	tc := it.ToolCall
	tc.Result = claudeResultText(b.Content)
	tc.Status = ToolOK
	if b.IsError {
		tc.Status = ToolError
	}
	if res.DurationSeconds != nil {
		tc.DurationMS = int64(*res.DurationSeconds * 1000)
	}
	if res.BackgroundTaskID != "" {
		c.startProcess(it, res)
	}
	tc.SubagentID = res.AgentID
	c.updateItem(e, it)
	delete(c.calls, b.ToolUseID)
	if res.AgentID != "" {
		c.startSubagent(e, it, res)
	}
}

// claudeResultText flattens tool_result.content: a string, or blocks of which
// only text has words to show.
func claudeResultText(raw json.RawMessage) string {
	text, blocks := decodeClaudeContent(raw)
	if text != "" {
		return text
	}
	var parts []string
	for i := range blocks {
		switch blocks[i].Type {
		case "text":
			parts = append(parts, blocks[i].Text)
		case "image":
			parts = append(parts, "[image]")
		}
	}
	return strings.Join(parts, "\n")
}

func (c *claude) startProcess(it *Item, res claudeToolResult) {
	id := res.BackgroundTaskID
	it.ToolCall.ProcessID = id
	it.Process = &Process{
		ID:      id,
		Command: claudeInputField(it, "command"),
		State:   ProcessRunning,
		Output:  OutputFile,
	}
	c.processes[id] = it
	path := res.OutputFile
	if path == "" {
		if m := claudeOutputPathRe.FindStringSubmatch(it.ToolCall.Result); m != nil {
			path = m[1]
		}
	}
	c.recordOutput(id, path)
}

func (c *claude) recordOutput(id, path string) {
	if path != "" {
		c.outputs[id] = path
	}
}

func claudeInputField(it *Item, key string) string {
	var fields map[string]any
	if json.Unmarshal([]byte(it.ToolCall.Input), &fields) != nil {
		return ""
	}
	s, _ := fields[key].(string)
	return s
}

// startSubagent adds the subagent item for an Agent result. A later result
// naming the same agent (a continued agent) links the call to the same item.
func (c *claude) startSubagent(e *claudeEvent, call *Item, res claudeToolResult) {
	id := res.AgentID
	c.recordOutput(id, res.OutputFile)
	if _, known := c.subagents[id]; known {
		return
	}
	started := e.at
	s := &Subagent{
		ID:          id,
		Name:        claudeInputField(call, "subagent_type"),
		Description: claudeInputField(call, "description"),
		State:       SubagentRunning,
		StartedAt:   &started,
	}
	finished := false
	if meta, ok := c.subagentMeta(id); ok {
		applySubagentMeta(s, meta)
		finished = meta.Finished
	}
	// A foreground agent's result is its end; no notification follows.
	if finished || res.Status == "completed" {
		endSubagent(s, SubagentDone, e.at)
	}
	c.subagents[id] = c.addItem(e, Item{Kind: KindSubagent, Subagent: s})
}

func (c *claude) subagentMeta(id string) (SubagentMeta, bool) {
	if c.opts.SubagentMeta == nil {
		return SubagentMeta{}, false
	}
	return c.opts.SubagentMeta(id)
}

func applySubagentMeta(s *Subagent, meta SubagentMeta) {
	if meta.Description != "" {
		s.Description = meta.Description
	}
	if meta.AgentType != "" {
		s.Name = meta.AgentType
	}
}

func endSubagent(s *Subagent, state string, at time.Time) {
	s.State = state
	s.EndedAt = &at
}
