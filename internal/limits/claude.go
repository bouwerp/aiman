package limits

import "time"

// NewClaudeSource reads what the status line wrapper recorded. Disabled means
// limits.claude_statusline is off. Stub until P4.
func NewClaudeSource(_ Env, _ bool) Source { return stubSource{"claude"} }

// RecordClaude stores only rate_limits from status line input, atomically at
// 0600. Input without them leaves the previous file. Stub until P4.
func RecordClaude(_ []byte, _ string, _ time.Time) error { return nil }
