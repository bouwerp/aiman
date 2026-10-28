package domain

import "context"

// Agent represents an AI coding agent available on the remote server.
type Agent struct {
	Name        string
	Command     string
	Description string
	// LaunchModel and LaunchEffort are the model and effort for this one
	// launch, from the agents.preferred entry serve picked. They override
	// agent_defaults and the repo's agent_overrides; a role override still
	// wins. Empty leaves the configured value in place.
	LaunchModel  string
	LaunchEffort string
}

// WithSessionLaunch returns a with the session's stored launch model and
// effort filled in where a names none, so a relaunch of the same agent keeps
// the values the session was created with.
func (a Agent) WithSessionLaunch(s Session) Agent {
	if a.LaunchModel == "" {
		a.LaunchModel = s.LaunchModel
	}
	if a.LaunchEffort == "" {
		a.LaunchEffort = s.LaunchEffort
	}
	return a
}

// AgentScanner defines the interface for scanning available agents on a remote server.
type AgentScanner interface {
	ScanAgents(ctx context.Context) ([]Agent, error)
}
