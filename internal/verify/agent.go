// Package verify runs a reviewer agent from a different vendor over a
// snapshot of a worker session's tree and turns its findings into to-dos.
package verify

import (
	"errors"
	"time"

	"github.com/bouwerp/aiman/internal/infra/agent"
)

// DefaultTimeout bounds one review when verify.timeout is unset.
const DefaultTimeout = 45 * time.Minute

// ErrNoReviewer means no candidate is installed or every installed one is the
// worker's own agent.
var ErrNoReviewer = errors.New("no installed reviewer agent differs from the worker's")

// PickReviewer chooses the reviewer's agent command. An installed override is
// used as is; otherwise the first installed candidate whose command differs
// from the worker's, so a model does not review its own work.
func PickReviewer(agents []string, workerAgent, override string, installed func(string) bool) (string, error) {
	if override != "" {
		if installed(override) {
			return override, nil
		}
		return "", ErrNoReviewer
	}
	worker := commandOf(workerAgent)
	for _, candidate := range agents {
		if commandOf(candidate) == worker || !installed(candidate) {
			continue
		}
		return candidate, nil
	}
	return "", ErrNoReviewer
}

// commandOf normalises an agent name or command to its binary, so a display
// name like "Claude Code" compares equal to "claude".
func commandOf(name string) string {
	if a, ok := agent.FindKnown(name); ok {
		return agent.CommandBase(a.Command)
	}
	return agent.CommandBase(name)
}
