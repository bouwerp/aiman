package domain

import "context"

// Agent represents an AI coding agent available on the remote server.
type Agent struct {
	Name        string
	Command     string
	Description string
}

// AgentScanner defines the interface for scanning available agents on a remote server.
type AgentScanner interface {
	ScanAgents(ctx context.Context) ([]Agent, error)
}
