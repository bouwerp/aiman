package transcript

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// codexNormaliser reads Codex rollout lines (docs/specs/phone-transcript.md,
// vendor table and rules 3, 6, 7, 9, 10). Positions are the lines' ordinals.
//
// Only lines named here produce items. Injected response_item user and
// developer messages, turn_context, base_instructions, world_state,
// encrypted_content and rate_limits are never decoded into an item.
type codexNormaliser struct {
	seen  map[string]bool // item ids already listed
	calls map[string]*Item
	open  []string // call ids with no output yet, oldest first
	// compactionListed is set by a compacted line, which the ContextCompaction
	// item for the same compaction follows a few lines later.
	compactionListed bool
}

type codexEnvelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexResponse struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Role      string          `json:"role"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Input     string          `json:"input"`
	Arguments string          `json:"arguments"`
	Output    json.RawMessage `json:"output"`
	Content   []codexText     `json:"content"`
	Summary   []codexText     `json:"summary"`
}

type codexEvent struct {
	Type string          `json:"type"`
	Item json.RawMessage `json:"item"`
}

type codexText struct {
	Text string `json:"text"`
}

// codexEventItem is an item_completed item of a type this normaliser reads.
// Other item types vary in shape, so the type is read before this is decoded.
type codexEventItem struct {
	Type             string      `json:"type"`
	ID               string      `json:"id"`
	Content          []codexText `json:"content"`
	SummaryText      []string    `json:"summary_text"`
	ProcessID        string      `json:"process_id"`
	Command          []string    `json:"command"`
	AggregatedOutput string      `json:"aggregated_output"`
	ExitCode         *int        `json:"exit_code"`
	Duration         struct {
		Secs  int64 `json:"secs"`
		Nanos int64 `json:"nanos"`
	} `json:"duration"`
	Tool   string `json:"tool"`
	Status string `json:"status"`
}

// NewCodex returns the Codex rollout normaliser.
func NewCodex() Normaliser {
	n := &codexNormaliser{}
	n.Reset()
	return n
}

func (n *codexNormaliser) Kind() string { return "codex" }

func (n *codexNormaliser) Capabilities() Capabilities {
	return Capabilities{
		Agent:       "codex",
		Scrollback:  ScrollbackTranscript,
		ToolResults: true,
		Thinking:    ThinkingSummary,
		Subagents:   true,
		Processes:   ProcessesInline,
	}
}

func (n *codexNormaliser) PositionKind() string { return PositionOrdinal }

func (n *codexNormaliser) Flush() []Delta { return nil }

func (n *codexNormaliser) Reset() {
	n.seen = map[string]bool{}
	n.calls = map[string]*Item{}
	n.open = nil
	n.compactionListed = false
}

func (n *codexNormaliser) Feed(pos int64, line []byte) []Delta {
	var env codexEnvelope
	if json.Unmarshal(line, &env) != nil {
		return nil
	}
	at := codexTime(env.Timestamp)
	switch env.Type {
	case "response_item":
		return n.feedResponse(pos, at, env.Payload)
	case "event_msg":
		return n.feedEvent(pos, at, env.Payload)
	case "compacted":
		n.compactionListed = true
		return n.add(Item{ID: codexLineID("compacted", pos), Kind: KindSystem, At: at, Rev: pos, Text: "Context compacted"})
	}
	return nil
}

func (n *codexNormaliser) feedResponse(pos int64, at time.Time, raw json.RawMessage) []Delta {
	var p codexResponse
	if json.Unmarshal(raw, &p) != nil {
		return nil
	}
	switch p.Type {
	case "message":
		if p.Role == "assistant" {
			return n.addText(p.ID, "assistant", KindAssistant, pos, at, codexJoin(p.Content, ""))
		}
	case "reasoning":
		return n.addText(p.ID, "reasoning", KindThinking, pos, at, codexJoin(p.Summary, "\n\n"))
	case "custom_tool_call":
		return n.startCall(pos, at, p, p.Input)
	case "function_call":
		return n.startCall(pos, at, p, p.Arguments)
	case "custom_tool_call_output", "function_call_output":
		return n.finishCall(pos, p)
	}
	return nil
}

func (n *codexNormaliser) feedEvent(pos int64, at time.Time, raw json.RawMessage) []Delta {
	var ev codexEvent
	if json.Unmarshal(raw, &ev) != nil {
		return nil
	}
	switch ev.Type {
	case "item_completed":
		return n.feedItem(pos, at, ev.Item)
	case "turn_aborted":
		return n.abortCalls(pos)
	}
	return nil
}

func (n *codexNormaliser) feedItem(pos int64, at time.Time, raw json.RawMessage) []Delta {
	var head struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &head) != nil {
		return nil
	}
	switch head.Type {
	case "UserMessage", "AgentMessage", "Reasoning", "CommandExecution", "ContextCompaction", "CollabAgentToolCall":
	default:
		return nil
	}
	var it codexEventItem
	if json.Unmarshal(raw, &it) != nil {
		return nil
	}
	switch it.Type {
	case "UserMessage":
		n.compactionListed = false
		return n.addText(it.ID, "user", KindUser, pos, at, codexJoin(it.Content, ""))
	case "AgentMessage":
		return n.addText(it.ID, "assistant", KindAssistant, pos, at, codexJoin(it.Content, ""))
	case "Reasoning":
		return n.addText(it.ID, "reasoning", KindThinking, pos, at, strings.Join(it.SummaryText, "\n\n"))
	case "CommandExecution":
		return n.feedCommand(pos, at, it)
	case "ContextCompaction":
		if n.compactionListed {
			n.compactionListed = false
			return nil
		}
		return n.addText(it.ID, "compaction", KindSystem, pos, at, "Context compacted")
	}
	return n.addText(it.ID, "collab", KindSystem, pos, at, strings.TrimSpace("Sub-agent "+it.Tool+" "+it.Status))
}

// addText lists a text item once. An AgentMessage or Reasoning event and its
// response_item line carry the same id, so whichever comes second adds nothing.
func (n *codexNormaliser) addText(id, prefix string, kind ItemKind, pos int64, at time.Time, text string) []Delta {
	if text == "" {
		return nil
	}
	if id == "" {
		id = codexLineID(prefix, pos)
	}
	return n.add(Item{ID: id, Kind: kind, At: at, Rev: pos, Text: text})
}

func (n *codexNormaliser) add(it Item) []Delta {
	if n.seen[it.ID] {
		return nil
	}
	n.seen[it.ID] = true
	return []Delta{{Add: &it}}
}

func (n *codexNormaliser) startCall(pos int64, at time.Time, p codexResponse, input string) []Delta {
	if p.CallID == "" || n.seen[p.CallID] {
		return nil
	}
	it := &Item{
		ID:   p.CallID,
		Kind: KindToolCall,
		At:   at,
		Rev:  pos,
		ToolCall: &ToolCall{
			ToolID:  p.CallID,
			Name:    p.Name,
			Summary: summariseToolInput(input),
			Input:   input,
			Status:  ToolRunning,
		},
	}
	n.calls[p.CallID] = it
	n.open = append(n.open, p.CallID)
	return n.add(*codexClone(it))
}

func (n *codexNormaliser) finishCall(pos int64, p codexResponse) []Delta {
	it := n.calls[p.CallID]
	if it == nil {
		return nil
	}
	it.ToolCall.Result = codexOutputText(p.Output)
	if it.ToolCall.Status == ToolRunning {
		it.ToolCall.Status = ToolOK
	}
	it.Rev = pos
	n.closeCall(p.CallID)
	return []Delta{{Update: it}}
}

func (n *codexNormaliser) closeCall(id string) {
	delete(n.calls, id)
	for i, open := range n.open {
		if open == id {
			n.open = append(n.open[:i], n.open[i+1:]...)
			return
		}
	}
}

// abortCalls ends every open call: their output will never come, and a call
// left running would keep the phone's live item on screen (rule 11).
func (n *codexNormaliser) abortCalls(pos int64) []Delta {
	var deltas []Delta
	for _, id := range n.open {
		it := n.calls[id]
		it.ToolCall.Status = ToolError
		it.Rev = pos
		deltas = append(deltas, Delta{Update: it})
		delete(n.calls, id)
	}
	n.open = nil
	return deltas
}

// feedCommand places a CommandExecution. Its id never equals a call_id, so it
// goes on the latest call with no output yet. That call's item holds one
// process, so any other command is listed as its own process item, keeping
// every process id reachable. A failed command still fails the open call it
// ran under, so the call's badge never hides it.
func (n *codexNormaliser) feedCommand(pos int64, at time.Time, ce codexEventItem) []Delta {
	proc := codexProcess(ce)
	failed := ce.ExitCode != nil && *ce.ExitCode != 0
	var call *Item
	if len(n.open) > 0 {
		call = n.calls[n.open[len(n.open)-1]]
	}
	if call != nil && call.Process == nil {
		call.Text = ce.AggregatedOutput
		call.Process = &proc
		call.ToolCall.ProcessID = proc.ID
		call.ToolCall.DurationMS = ce.Duration.Secs*1000 + ce.Duration.Nanos/1e6
		if failed {
			call.ToolCall.Status = ToolError
		}
		call.Rev = pos
		return []Delta{{Update: codexClone(call)}}
	}
	id := ce.ID
	if id == "" {
		id = codexLineID("process", pos)
	}
	deltas := n.add(Item{ID: id, Kind: KindProcess, At: at, Rev: pos, Text: ce.AggregatedOutput, Process: &proc})
	if call != nil && failed {
		call.ToolCall.Status = ToolError
		call.Rev = pos
		deltas = append(deltas, Delta{Update: codexClone(call)})
	}
	return deltas
}

func codexProcess(ce codexEventItem) Process {
	p := Process{
		ID:       ce.ProcessID,
		Command:  strings.Join(ce.Command, " "),
		State:    ProcessUnknown,
		ExitCode: ce.ExitCode,
		Output:   OutputInline,
	}
	if p.ID == "" {
		p.ID = ce.ID
	}
	if ce.ExitCode != nil {
		p.State = ProcessExited
	}
	return p
}

// codexOutputText reads a tool output: a string, a list of text parts, or, for
// a shape not seen in rollouts so far, the raw JSON.
func codexOutputText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []codexText
	if json.Unmarshal(raw, &parts) == nil {
		return codexJoin(parts, "\n")
	}
	return string(raw)
}

func codexJoin(parts []codexText, sep string) string {
	texts := make([]string, 0, len(parts))
	for _, p := range parts {
		texts = append(texts, p.Text)
	}
	return strings.Join(texts, sep)
}

// codexLineID names an item whose line carries no id of its own. A position is
// unique within a rollout and stable across a reread.
func codexLineID(prefix string, pos int64) string {
	return prefix + "-" + strconv.FormatInt(pos, 10)
}

func codexTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// codexClone copies an item so a delta never aliases state the normaliser later
// changes.
func codexClone(it *Item) *Item {
	c := *it
	if it.ToolCall != nil {
		tc := *it.ToolCall
		c.ToolCall = &tc
	}
	if it.Process != nil {
		p := *it.Process
		c.Process = &p
	}
	return &c
}
