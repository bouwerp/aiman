package transcript

import (
	"strings"
	"time"
)

// GrokOptions configures the Grok normaliser.
type GrokOptions struct {
	// SessionDir is the session folder, ~/.grok/sessions/<cwd>/<id>. It
	// holds subagents/<id>/meta.json, and child sessions are its siblings.
	// Empty skips both lookups; sub-agents still come from the parent lines.
	SessionDir string
}

// ProcessPather is implemented by normalisers whose processes write to a
// file. ProcessPath returns the output path the transcript named for a
// process id. The path is untrusted: callers confine it before reading.
type ProcessPather interface {
	ProcessPath(id string) (path string, ok bool)
}

// NewGrok returns the normaliser for Grok updates.jsonl files.
func NewGrok() Normaliser {
	return NewGrokWith(GrokOptions{})
}

// NewGrokWith returns the Grok normaliser with options.
func NewGrokWith(opts GrokOptions) Normaliser {
	g := &grokNormaliser{opts: opts}
	g.Reset()
	return g
}

// grokNormaliser turns Grok's ACP session updates into items. Stored items
// are mutated in place; every delta carries a copy. Chunk groups emit their
// Add on the first chunk and one Update per Flush, so a long streamed reply
// costs one copy per read rather than one per chunk.
type grokNormaliser struct {
	opts GrokOptions

	groups    map[string]*grokGroup // chunk groups by item id
	open      map[ItemKind]string   // open group key per chunk kind
	dirty     []*grokGroup          // groups changed since the last Flush
	user      *Item                 // open user message, joined across chunks
	calls     map[string]*Item      // tool_call items by toolCallId
	subagents map[string]*Item      // subagent items by subagent_id
	processes map[string]*Item      // process items by task_id
	outputs   map[string]string     // output_file by task_id
}

func (g *grokNormaliser) Kind() string { return "grok" }

func (g *grokNormaliser) Capabilities() Capabilities {
	return Capabilities{
		Agent:       "grok",
		Scrollback:  ScrollbackTranscript,
		ToolResults: true,
		Thinking:    ThinkingFull,
		Subagents:   true,
		Processes:   ProcessesFile,
	}
}

func (g *grokNormaliser) PositionKind() string { return PositionOffset }

func (g *grokNormaliser) Reset() {
	g.groups = map[string]*grokGroup{}
	g.open = map[ItemKind]string{}
	g.dirty = nil
	g.user = nil
	g.calls = map[string]*Item{}
	g.subagents = map[string]*Item{}
	g.processes = map[string]*Item{}
	g.outputs = map[string]string{}
}

func (g *grokNormaliser) ProcessPath(id string) (string, bool) {
	p, ok := g.outputs[id]
	return p, ok
}

// Feed handles one updates.jsonl line. Lines that are not session updates,
// do not parse, or carry an unknown sessionUpdate give no deltas.
func (g *grokNormaliser) Feed(pos int64, line []byte) []Delta {
	l, ok := parseGrokLine(pos, line)
	if !ok {
		return nil
	}
	if l.upd.SessionUpdate != "user_message_chunk" {
		g.user = nil
	}
	switch l.upd.SessionUpdate {
	case "user_message_chunk":
		return g.userChunk(l)
	case "agent_message_chunk":
		return g.chunk(l, KindAssistant)
	case "agent_thought_chunk":
		return g.chunk(l, KindThinking)
	case "turn_completed":
		g.open = map[ItemKind]string{}
		return nil
	}
	return g.event(l)
}

// event handles the updates that are not chunks.
func (g *grokNormaliser) event(l *grokLine) []Delta {
	switch l.upd.SessionUpdate {
	case "tool_call":
		return g.toolCall(l)
	case "tool_call_update":
		return g.toolUpdate(l)
	case "subagent_spawned":
		return g.subagentSpawned(l)
	case "subagent_finished":
		return g.subagentFinished(l)
	case "task_backgrounded":
		return g.taskBackgrounded(l)
	case "task_completed":
		return g.taskCompleted(l)
	case "hook_execution":
		return g.hookExecution(l)
	}
	if text := grokCompactionText(&l.upd); text != "" {
		return g.system(l, text)
	}
	return nil
}

// Flush emits one Update per chunk group changed since the last Flush.
func (g *grokNormaliser) Flush() []Delta {
	var out []Delta
	for _, gr := range g.dirty {
		gr.item.Text = gr.text.String()
		out = append(out, Delta{Update: cloneClaudeItem(&gr.item)})
		gr.dirty = false
	}
	g.dirty = nil
	return out
}

func (g *grokNormaliser) userChunk(l *grokLine) []Delta {
	text := l.upd.chunkText()
	if g.user != nil {
		g.user.Text += text
		return g.update(l, g.user)
	}
	it := &Item{ID: "user:" + l.key(), Kind: KindUser, Text: text}
	g.user = it
	return g.add(l, it)
}

func (g *grokNormaliser) system(l *grokLine, text string) []Delta {
	return g.add(l, &Item{ID: "system:" + l.key(), Kind: KindSystem, Text: text})
}

// add stamps a new item with the line's time and position and emits it.
func (g *grokNormaliser) add(l *grokLine, it *Item) []Delta {
	it.At = l.at
	it.Rev = l.pos
	return []Delta{{Add: cloneClaudeItem(it)}}
}

// update moves a stored item's rev to the line and emits its new state.
func (g *grokNormaliser) update(l *grokLine, it *Item) []Delta {
	it.Rev = l.pos
	return []Delta{{Update: cloneClaudeItem(it)}}
}

// grokGroup is one joined run of message or thought chunks.
type grokGroup struct {
	item  Item
	text  strings.Builder
	dirty bool
}

// chunk joins a message or thought chunk into its group. The group key is
// the promptId, else the kind's open group, else the chunk's eventId; the
// item id is the key with a per-kind prefix, so it is stable across reads.
func (g *grokNormaliser) chunk(l *grokLine, kind ItemKind) []Delta {
	key := l.meta.PromptID
	if key == "" {
		key = g.open[kind]
	}
	if key == "" {
		key = l.key()
	}
	g.open[kind] = key
	id := grokChunkPrefix(kind) + key
	text := l.upd.chunkText()
	if gr, ok := g.groups[id]; ok {
		gr.text.WriteString(text)
		gr.item.Rev = l.pos
		if !gr.dirty {
			gr.dirty = true
			g.dirty = append(g.dirty, gr)
		}
		return nil
	}
	gr := &grokGroup{item: Item{ID: id, Kind: kind, At: l.at, Rev: l.pos, Text: text}}
	gr.text.WriteString(text)
	g.groups[id] = gr
	return []Delta{{Add: cloneClaudeItem(&gr.item)}}
}

func grokChunkPrefix(kind ItemKind) string {
	if kind == KindThinking {
		return "thinking:"
	}
	return "assistant:"
}

// grokTime is the line's agent timestamp, else its whole-second timestamp.
func grokTime(ms int64, secs float64) time.Time {
	switch {
	case ms > 0:
		return time.UnixMilli(ms)
	case secs > 0:
		return time.Unix(int64(secs), 0)
	}
	return time.Time{}
}
