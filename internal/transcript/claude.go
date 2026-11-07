package transcript

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
func NewClaudeWith(ClaudeOptions) Normaliser {
	return &claude{}
}

// ClaudeMetaLookup returns a SubagentMeta lookup reading
// <sessionDir>/subagents/agent-<agentId>.meta.json. An agentId that is not
// a plain file name component, or a missing or unreadable file, is not found.
func ClaudeMetaLookup(sessionDir string) func(agentID string) (SubagentMeta, bool) {
	return func(string) (SubagentMeta, bool) { return SubagentMeta{}, false }
}

type claude struct{}

func (c *claude) Kind() string                        { return "" }
func (c *claude) Capabilities() Capabilities          { return Capabilities{} }
func (c *claude) PositionKind() string                { return "" }
func (c *claude) Feed(pos int64, line []byte) []Delta { return nil }
func (c *claude) Flush() []Delta                      { return nil }
func (c *claude) Reset()                              {}

func (c *claude) ProcessOutput(id string) (string, bool) { return "", false }
