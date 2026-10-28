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

// AgentScanner defines the interface for scanning available agents on a remote server.
type AgentScanner interface {
	ScanAgents(ctx context.Context) ([]Agent, error)
}
