package transcript

import "fmt"

// codexNormaliser reads Codex rollout lines (docs/specs/phone-transcript.md,
// vendor table and rules 3, 6, 7, 9, 10). Positions are the lines' ordinals.
type codexNormaliser struct{}

// NewCodex returns the Codex rollout normaliser.
func NewCodex() Normaliser { return &codexNormaliser{} }

func (n *codexNormaliser) Kind() string                        { return "codex" }
func (n *codexNormaliser) Capabilities() Capabilities          { return Capabilities{} }
func (n *codexNormaliser) PositionKind() string                { return "" }
func (n *codexNormaliser) Feed(pos int64, line []byte) []Delta { return nil }
func (n *codexNormaliser) Flush() []Delta                      { return nil }
func (n *codexNormaliser) Reset()                              {}

// CodexChildHead reads the head of a Codex child rollout: the parent's
// thread id from session_meta.payload.parent_thread_id, and a Subagent whose
// ID is the child's thread id, Name the spawn nickname (else the
// source.subagent string), StartedAt the session_meta time, and State done
// with EndedAt set when the rollout holds task_complete (or ends on a final
// AgentMessage), else running. A head without parent_thread_id is an error.
func CodexChildHead(path string) (parentThreadID string, sa Subagent, err error) {
	return "", Subagent{}, fmt.Errorf("codex child head %s: %w", path, errNotImplemented)
}
