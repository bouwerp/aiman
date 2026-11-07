package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SubagentMeta is what a Claude sub-agent's
// <session>/subagents/agent-<agentId>.meta.json says.
type SubagentMeta struct {
	AgentType     string `json:"agentType"`
	Description   string `json:"description"`
	ToolUseID     string `json:"toolUseId"`
	Finished      bool   `json:"finished"`
	ParentAgentID string `json:"parentAgentId,omitempty"`
}

// ClaudeOptions configures the Claude normaliser.
//
// SubagentMeta, when set, is how the normaliser learns a sub-agent's
// metadata: it is asked when an Agent result names an agentId, and again
// when a task-notification names that agent. Its description and agentType
// fill the subagent item, and Finished moves its state to done. Without it
// the description comes from the Agent call's input and the state from the
// task-notification's status. The normaliser itself reads no files.
type ClaudeOptions struct {
	SubagentMeta func(agentID string) (SubagentMeta, bool)
}

// ProcessOutputs is implemented by normalisers whose background processes
// write an output file: it returns the path the transcript named for a
// process id. The path is untrusted; the caller confines it.
type ProcessOutputs interface {
	ProcessOutput(id string) (path string, ok bool)
}

// NewClaude returns the Claude Code normaliser with no sub-agent lookup.
func NewClaude() Normaliser {
	return NewClaudeWith(ClaudeOptions{})
}

// NewClaudeWith returns the Claude Code normaliser. It also implements
// ProcessOutputs.
func NewClaudeWith(opts ClaudeOptions) Normaliser {
	c := &claude{opts: opts}
	c.Reset()
	return c
}

// ClaudeMetaLookup returns a SubagentMeta lookup reading
// <sessionDir>/subagents/agent-<agentId>.meta.json. An agentId that is not
// a plain file name component, or a missing or unreadable file, is not found.
func ClaudeMetaLookup(sessionDir string) func(agentID string) (SubagentMeta, bool) {
	return func(agentID string) (SubagentMeta, bool) {
		if agentID == "" || agentID == "." || agentID == ".." || strings.ContainsAny(agentID, `/\`) {
			return SubagentMeta{}, false
		}
		raw, err := os.ReadFile(filepath.Join(sessionDir, "subagents", "agent-"+agentID+".meta.json"))
		if err != nil {
			return SubagentMeta{}, false
		}
		var meta SubagentMeta
		if json.Unmarshal(raw, &meta) != nil {
			return SubagentMeta{}, false
		}
		return meta, true
	}
}

// claude holds the conversation tree and the items later lines can change.
// Stored items are mutated in place; every delta carries a copy.
type claude struct {
	opts  ClaudeOptions
	nodes map[string]*claudeNode
	root  *claudeNode // parent of every line with a null parentUuid
	tip   *claudeNode // last line that changed an item

	calls     map[string]*Item // tool_call items by tool_use id
	subagents map[string]*Item // subagent items by agent id
	processes map[string]*Item // tool_call items by background task id
	outputs   map[string]string
}

func (c *claude) Kind() string { return "claude" }

func (c *claude) Capabilities() Capabilities {
	return Capabilities{
		Agent:       "claude",
		Scrollback:  ScrollbackTranscript,
		ToolResults: true,
		Thinking:    ThinkingFull,
		Subagents:   true,
		Processes:   ProcessesFile,
	}
}

func (c *claude) PositionKind() string { return PositionOffset }

// Flush returns nothing: Claude holds no open groups across lines.
func (c *claude) Flush() []Delta { return nil }

func (c *claude) Reset() {
	c.nodes = map[string]*claudeNode{}
	c.root = &claudeNode{}
	c.tip = nil
	c.calls = map[string]*Item{}
	c.subagents = map[string]*Item{}
	c.processes = map[string]*Item{}
	c.outputs = map[string]string{}
}

func (c *claude) ProcessOutput(id string) (string, bool) {
	path, ok := c.outputs[id]
	return path, ok
}

// Feed turns one line into deltas. A line with no uuid is a header or
// bookkeeping record; a uuid already seen is a replay and is ignored.
func (c *claude) Feed(pos int64, line []byte) []Delta {
	e, ok := parseClaudeLine(pos, line)
	if !ok {
		return nil
	}
	if _, dup := c.nodes[e.UUID]; dup {
		return nil
	}
	if e.opensTurn() {
		c.rewind(e)
	}
	c.link(e)
	if !e.IsMeta {
		c.apply(e)
	}
	if e.changed {
		c.tip = e.node
	}
	return e.deltas
}

func (c *claude) apply(e *claudeEvent) {
	switch e.Type {
	case "system":
		if e.Subtype == claudeCompactBoundary {
			c.addItem(e, Item{Kind: KindSystem, Text: "Conversation compacted"})
		}
	case "assistant":
		c.assistantLine(e)
	case "user":
		c.userLine(e)
	}
}

func (c *claude) assistantLine(e *claudeEvent) {
	for i := range e.blocks {
		b := &e.blocks[i]
		switch b.Type {
		case "text":
			if b.Text != "" {
				c.addItem(e, Item{Kind: KindAssistant, Text: b.Text})
			}
		case "thinking":
			c.addItem(e, Item{Kind: KindThinking, Text: b.Thinking, Hidden: b.Thinking == ""})
		case "redacted_thinking":
			c.addItem(e, Item{Kind: KindThinking, Hidden: true})
		case "tool_use":
			c.toolUse(e, b)
		}
	}
}

func (c *claude) userLine(e *claudeEvent) {
	switch {
	case e.IsCompactSummary:
		c.addItem(e, Item{Kind: KindSystem, Text: e.plainText()})
	case e.isNotification():
		c.notification(e)
	case e.text != "":
		c.addItem(e, Item{Kind: KindUser, Text: e.text})
	default:
		c.userBlocks(e)
	}
}

func (c *claude) userBlocks(e *claudeEvent) {
	for i := range e.blocks {
		b := &e.blocks[i]
		switch b.Type {
		case "text":
			if b.Text != "" {
				c.addItem(e, Item{Kind: KindUser, Text: b.Text})
			}
		case "tool_result":
			c.toolResult(e, b)
		}
	}
}

// addItem appends it to the line's node. The id is the line's uuid plus the
// item's place on that line, so a reread gives the same id.
func (c *claude) addItem(e *claudeEvent, it Item) *Item {
	it.ID = e.UUID + "#" + strconv.Itoa(len(e.node.items))
	it.At = e.at
	it.Rev = e.pos
	e.node.items = append(e.node.items, it.ID)
	e.changed = true
	e.deltas = append(e.deltas, Delta{Add: cloneClaudeItem(&it)})
	return &it
}

// updateItem moves a stored item's rev to this line and emits its new state.
func (c *claude) updateItem(e *claudeEvent, it *Item) {
	it.Rev = e.pos
	e.changed = true
	e.deltas = append(e.deltas, Delta{Update: cloneClaudeItem(it)})
}

func cloneClaudeItem(it *Item) *Item {
	out := *it
	if it.ToolCall != nil {
		tc := *it.ToolCall
		out.ToolCall = &tc
	}
	if it.Subagent != nil {
		s := *it.Subagent
		out.Subagent = &s
	}
	if it.Process != nil {
		p := *it.Process
		out.Process = &p
	}
	return &out
}
