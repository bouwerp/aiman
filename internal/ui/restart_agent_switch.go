package ui

import (
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
)

// adoptRestartAgent points a session at the agent it is about to be restarted
// with, dropping the previous agent's identity when they differ. It returns
// true when the agent changed (caller must also clear the remote sidecar and
// skip native --resume).
//
// A session carries the running agent's native session id, transcript path,
// title and hook state. None of that describes a different agent, and a latched
// AgentEnded would render the freshly started session as already exited. A
// restart with the same agent is a resume, so its transcript and state are still
// its own and are kept.
func adoptRestartAgent(s *domain.Session, agentName string) bool {
	if s == nil {
		return false
	}
	switched := s.AgentName != "" && !strings.EqualFold(s.AgentName, agentName)
	if switched {
		s.AgentSessionID = ""
		s.AgentSessionPath = ""
		s.AgentTitle = ""
		s.AgentEnded = false
		s.HookState = ""
		s.HookStateMessage = ""
		s.HookStateSource = ""
		s.HookStateSeq = 0
	}
	s.AgentName = agentName
	return switched
}
