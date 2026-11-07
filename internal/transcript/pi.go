package transcript

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// piNormaliser reads Pi session lines (docs/specs/phone-transcript.md,
// vendor table and rules 3, 5, 6, 7). Positions are byte offsets. Entries
// form an id/parentId tree; only the active branch is listed.
//
// Every entry stays in memory with its items, because moving back onto an
// abandoned branch has to list them again.
type piNormaliser struct {
	entries map[string]*piEntry
	calls   map[string]*piCall // by toolCall id
	leaf    string             // entry the active branch ends at
}

// piEntry is one node of the session tree. Entries that list nothing, such as
// the system prompt, still hold the chain together.
type piEntry struct {
	id     string
	parent string
	items  []*Item
	result *piResult
}

// piCall ties a tool call's item to the entry that listed it.
type piCall struct {
	item  *Item
	entry string
}

// piResult is what a toolResult message says about its call.
type piResult struct {
	callID     string
	text       string
	isError    bool
	durationMS int64
}

type piLine struct {
	Type          string     `json:"type"`
	ID            string     `json:"id"`
	ParentID      string     `json:"parentId"`
	Timestamp     string     `json:"timestamp"`
	Provider      string     `json:"provider"`
	ModelID       string     `json:"modelId"`
	ThinkingLevel string     `json:"thinkingLevel"`
	Message       *piMessage `json:"message"`
}

type piMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"toolCallId"`
	IsError    bool            `json:"isError"`
	DurationMS int64           `json:"durationMs"`
}

// piBlock leaves thinkingSignature undecoded so it can never reach an item.
type piBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// NewPi returns the Pi session normaliser.
func NewPi() Normaliser {
	n := &piNormaliser{}
	n.Reset()
	return n
}

func (n *piNormaliser) Kind() string { return "pi" }

func (n *piNormaliser) Capabilities() Capabilities {
	return Capabilities{
		Agent:       "pi",
		Scrollback:  ScrollbackTranscript,
		ToolResults: true,
		Thinking:    ThinkingFull,
		Subagents:   false,
		Processes:   ProcessesNone,
	}
}

func (n *piNormaliser) PositionKind() string { return PositionOffset }

func (n *piNormaliser) Flush() []Delta { return nil }

func (n *piNormaliser) Reset() {
	n.entries = map[string]*piEntry{}
	n.calls = map[string]*piCall{}
	n.leaf = ""
}

func (n *piNormaliser) Feed(pos int64, line []byte) []Delta {
	var l piLine
	if json.Unmarshal(line, &l) != nil || l.Type == "session" || l.ID == "" || n.entries[l.ID] != nil {
		return nil
	}
	// A parent this file never named cannot be placed, so the entry continues
	// the active branch rather than discarding it.
	parent := l.ParentID
	if parent != "" && n.entries[parent] == nil {
		parent = n.leaf
	}
	entry := &piEntry{id: l.ID, parent: parent}
	n.entries[l.ID] = entry

	deltas := n.moveTo(pos, parent)
	at := piTime(l.Timestamp)
	entry.items, entry.result = n.parseEntry(pos, at, l)
	n.leaf = l.ID
	deltas = append(deltas, n.list(entry)...)
	return append(deltas, n.applyResult(pos, entry)...)
}

// parseEntry builds the items and the result one line contributes.
func (n *piNormaliser) parseEntry(pos int64, at time.Time, l piLine) ([]*Item, *piResult) {
	switch l.Type {
	case "model_change":
		return piSystem(l.ID, pos, at, "Model: "+strings.Trim(l.Provider+"/"+l.ModelID, "/")), nil
	case "thinking_level_change":
		return piSystem(l.ID, pos, at, "Thinking level: "+l.ThinkingLevel), nil
	case "message":
		if l.Message != nil {
			return n.parseMessage(pos, at, l.ID, l.Message)
		}
	}
	return nil, nil
}

func (n *piNormaliser) parseMessage(pos int64, at time.Time, id string, m *piMessage) ([]*Item, *piResult) {
	blocks := piBlocks(m.Content)
	switch m.Role {
	case "user":
		if text := piJoinText(blocks, ""); text != "" {
			return []*Item{{ID: id, Kind: KindUser, At: at, Rev: pos, Text: text}}, nil
		}
	case "assistant":
		return n.parseAssistant(pos, at, id, blocks), nil
	case "toolResult":
		return nil, &piResult{
			callID:     m.ToolCallID,
			text:       piJoinText(blocks, "\n"),
			isError:    m.IsError,
			durationMS: m.DurationMS,
		}
	}
	return nil, nil
}

// parseAssistant keeps the blocks in order, so thinking stays ahead of the
// text or tool call it led to.
func (n *piNormaliser) parseAssistant(pos int64, at time.Time, id string, blocks []piBlock) []*Item {
	var items []*Item
	for i, b := range blocks {
		it := &Item{ID: id + ":" + strconv.Itoa(i), At: at, Rev: pos}
		switch {
		case b.Type == "text" && b.Text != "":
			it.Kind, it.Text = KindAssistant, b.Text
		case b.Type == "thinking" && b.Thinking != "":
			it.Kind, it.Text = KindThinking, b.Thinking
		case b.Type == "toolCall" && b.ID != "":
			it.Kind = KindToolCall
			it.ToolCall = &ToolCall{
				ToolID:  b.ID,
				Name:    b.Name,
				Summary: summariseToolInput(string(b.Arguments)),
				Input:   string(b.Arguments),
				Status:  ToolRunning,
			}
		default:
			continue
		}
		items = append(items, it)
	}
	return items
}

// list adds an entry's items and registers its tool calls.
func (n *piNormaliser) list(e *piEntry) []Delta {
	var deltas []Delta
	for _, it := range e.items {
		if it.ToolCall != nil {
			n.calls[it.ToolCall.ToolID] = &piCall{item: it, entry: e.id}
		}
		deltas = append(deltas, Delta{Add: piClone(it)})
	}
	return deltas
}

// applyResult updates the call a toolResult entry answers.
func (n *piNormaliser) applyResult(pos int64, e *piEntry) []Delta {
	if e.result == nil {
		return nil
	}
	call := n.calls[e.result.callID]
	if call == nil {
		return nil
	}
	tc := call.item.ToolCall
	tc.Result = e.result.text
	tc.DurationMS = e.result.durationMS
	tc.Status = ToolOK
	if e.result.isError {
		tc.Status = ToolError
	}
	call.item.Rev = pos
	return []Delta{{Update: piClone(call.item)}}
}

// moveTo makes parent the active branch's end. Entries leaving the branch are
// removed, and entries on an earlier abandoned branch that parent descends
// from are listed again, oldest first, so the list stays in chain order.
func (n *piNormaliser) moveTo(pos int64, parent string) []Delta {
	if parent == n.leaf {
		return nil
	}
	chain := map[string]bool{}
	var entering []*piEntry
	for id := parent; id != ""; id = n.entries[id].parent {
		chain[id] = true
		entering = append(entering, n.entries[id])
	}
	leaving := map[string]bool{}
	var deltas []Delta
	var gone []*piEntry
	for id := n.leaf; id != "" && !chain[id]; id = n.entries[id].parent {
		e := n.entries[id]
		leaving[id] = true
		gone = append(gone, e)
		for i := len(e.items) - 1; i >= 0; i-- {
			deltas = append(deltas, Delta{Remove: e.items[i].ID})
		}
	}
	for _, e := range gone {
		deltas = append(deltas, n.revertResult(pos, e, leaving)...)
	}
	return append(deltas, n.relist(pos, entering, n.keptDepth(entering))...)
}

// keptDepth is how many of entering, which runs from the new end towards the
// root, are not currently listed. The active chain is the old leaf's ancestry.
func (n *piNormaliser) keptDepth(entering []*piEntry) int {
	active := map[string]bool{}
	for id := n.leaf; id != ""; id = n.entries[id].parent {
		active[id] = true
	}
	for i, e := range entering {
		if active[e.id] {
			return i
		}
	}
	return len(entering)
}

// relist adds back the first count entries of entering, oldest first. Their
// rev moves to pos, so an `after` reader past their first listing sees them.
func (n *piNormaliser) relist(pos int64, entering []*piEntry, count int) []Delta {
	var deltas []Delta
	for i := count - 1; i >= 0; i-- {
		e := entering[i]
		for _, it := range e.items {
			it.Rev = pos
		}
		deltas = append(deltas, n.list(e)...)
		deltas = append(deltas, n.applyResult(pos, e)...)
	}
	return deltas
}

// revertResult undoes a result that left the branch. The call goes back to
// running; a reader only hears about it if the call itself stays listed.
func (n *piNormaliser) revertResult(pos int64, e *piEntry, leaving map[string]bool) []Delta {
	if e.result == nil {
		return nil
	}
	call := n.calls[e.result.callID]
	if call == nil {
		return nil
	}
	tc := call.item.ToolCall
	tc.Result, tc.DurationMS, tc.Status = "", 0, ToolRunning
	if leaving[call.entry] {
		return nil
	}
	call.item.Rev = pos
	return []Delta{{Update: piClone(call.item)}}
}

func piSystem(id string, pos int64, at time.Time, text string) []*Item {
	return []*Item{{ID: id, Kind: KindSystem, At: at, Rev: pos, Text: text}}
}

// piBlocks reads message content, which is a block list or a plain string.
func piBlocks(raw json.RawMessage) []piBlock {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []piBlock{{Type: "text", Text: text}}
	}
	var blocks []piBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	return blocks
}

func piJoinText(blocks []piBlock, sep string) string {
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, sep)
}

func piTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// piClone copies an item so a delta never aliases state the normaliser later
// changes.
func piClone(it *Item) *Item {
	c := *it
	if it.ToolCall != nil {
		tc := *it.ToolCall
		c.ToolCall = &tc
	}
	return &c
}
