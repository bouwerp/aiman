package agent

import (
	"context"
	"fmt"

	"github.com/bouwerp/aiman/internal/domain"
	tea "github.com/charmbracelet/bubbletea"
)

// knownAgents defines the agents we know how to scan for.
var knownAgents = []domain.Agent{
	{
		Name:        "Claude Code",
		Command:     "claude",
		Description: "Anthropic's Claude Code CLI",
	},
	{
		Name:        "Gemini CLI",
		Command:     "gemini",
		Description: "Google's Gemini CLI",
	},
	{
		Name:        "OpenCode",
		Command:     "opencode-cli",
		Description: "OpenCode interactive CLI",
	},
	{
		Name:        "GitHub Copilot CLI",
		Command:     "gh copilot",
		Description: "GitHub Copilot in the CLI",
	},
	{
		Name:        "Cursor",
		Command:     "cursor",
		Description: "Cursor AI Code Editor CLI",
	},
}

// Executor defines the interface for executing remote commands.
type Executor interface {
	Execute(ctx context.Context, cmd string) (string, error)
}

// Scanner scans for available agents on a remote server.
type Scanner struct {
	executor Executor
}

// NewScanner creates a new agent scanner.
func NewScanner(executor Executor) *Scanner {
	return &Scanner{
		executor: executor,
	}
}

// ScanAgents checks which agents are available on the remote server.
// It returns a list of agents that have their binaries in PATH.
func (s *Scanner) ScanAgents(ctx context.Context) ([]domain.Agent, error) {
	var available []domain.Agent

	for _, agent := range knownAgents {
		// Check if the command exists by running "which" or "command -v"
		checkCmd := fmt.Sprintf("command -v %s", agent.Command)
		_, err := s.executor.Execute(ctx, checkCmd)
		if err == nil {
			// Command exists in PATH
			available = append(available, agent)
		}
	}

	return available, nil
}

// ScanAgentsMsg is a bubbletea message containing the scan results.
type ScanAgentsMsg struct {
	Agents []domain.Agent
	Err    error
}

// ScanCmd returns a bubbletea command that performs the agent scan.
func ScanCmd(scanner domain.AgentScanner) func() tea.Msg {
	return func() tea.Msg {
		ctx := context.Background()
		agents, err := scanner.ScanAgents(ctx)
		return ScanAgentsMsg{
			Agents: agents,
			Err:    err,
		}
	}
}
