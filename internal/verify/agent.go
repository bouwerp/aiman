// Package verify runs a reviewer agent from a different vendor over a
// snapshot of a worker session's tree and turns its findings into to-dos.
package verify

import (
	"errors"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// DefaultTimeout bounds one review when verify.timeout is unset.
const DefaultTimeout = 45 * time.Minute

// ErrNoReviewer means no candidate is installed or every installed one is the
// worker's own agent.
var ErrNoReviewer = errors.New("no installed reviewer agent differs from the worker's")

// PickReviewer chooses the reviewer's entry. An installed override is used as
// is; otherwise the first installed candidate that differs from the worker's
// launch, so a model does not review its own work.
//
// A candidate that names a model differs from the worker whenever the model
// differs too: one binary on another model is an independent reviewer. A bare
// candidate keeps the binary-only rule, so an all-Pi setup still gets a
// reviewer from agents.smart.
func PickReviewer(agents []config.PreferredAgent, workerAgent, workerModel, override string, installed func(string) bool) (config.PreferredAgent, error) {
	if override != "" {
		if installed(override) {
			return config.PreferredAgent{Agent: override}, nil
		}
		return config.PreferredAgent{}, ErrNoReviewer
	}
	worker := commandOf(workerAgent)
	for _, candidate := range agents {
		command := candidate.Command()
		if command == "" || !installed(command) {
			continue
		}
		if commandOf(command) == worker && (candidate.Model == "" || strings.TrimSpace(candidate.Model) == strings.TrimSpace(workerModel)) {
			continue
		}
		candidate.Agent = command
		return candidate, nil
	}
	return config.PreferredAgent{}, ErrNoReviewer
}

// commandOf normalises an agent name or command to its binary, so a display
// name like "Claude Code" compares equal to "claude".
func commandOf(name string) string {
	if a, ok := agent.FindKnown(name); ok {
		return agent.CommandBase(a.Command)
	}
	return agent.CommandBase(name)
}
