package agentupdate

import (
	"fmt"

	"github.com/bouwerp/aiman/internal/domain"
)

// LiveSession is one running session as the gate sees it: the agent base
// command and the last hook-reported state.
type LiveSession struct {
	Agent     string
	HookState string
}

// Gate decides whether agent may be updated now. A swap-safe install waits
// while any live session of that agent is working; an in-place install waits
// while any live session of that agent exists. When it waits, reason reads
// like "deferred: 2 sessions busy".
func Gate(agent string, method Method, sessions []LiveSession) (ok bool, reason string) {
	swap := method.SwapSafe()
	n := 0
	for _, s := range sessions {
		if s.Agent != agent {
			continue
		}
		if swap && s.HookState != string(domain.AgentStateWorking) {
			continue
		}
		n++
	}
	if n == 0 {
		return true, ""
	}
	word := "running"
	if swap {
		word = "busy"
	}
	return false, fmt.Sprintf("deferred: %d %s %s", n, plural(n, "session", "sessions"), word)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
