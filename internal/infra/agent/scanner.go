package agent

import (
	"context"
	"fmt"
	"strings"

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
		// We try multiple ways to find the agent:
		// 1. Using a login shell (to load user profiles like .bashrc, .zshrc)
		// 2. Direct command check (in case login shell fails or is limited)
		
		found := false
		baseCmd := strings.Split(agent.Command, " ")[0]
		
		// Try login shell first
		checkCmd := fmt.Sprintf("bash -lc 'command -v %s'", baseCmd)
		_, err := s.executor.Execute(ctx, checkCmd)
		if err == nil {
			found = true
		} else {
			// Try direct check as fallback
			checkCmd = fmt.Sprintf("command -v %s", baseCmd)
			_, err = s.executor.Execute(ctx, checkCmd)
			if err == nil {
				found = true
			}
		}

		if found {
			// If it's a multi-word command like "gh copilot", we should verify the extension exists
			if strings.Contains(agent.Command, " ") {
				// Try verifying with a simple flag
				verifyCmd := fmt.Sprintf("bash -lc '%s --version'", agent.Command)
				_, err = s.executor.Execute(ctx, verifyCmd)
				if err != nil {
					// Try without login shell
					verifyCmd = fmt.Sprintf("%s --version", agent.Command)
					_, err = s.executor.Execute(ctx, verifyCmd)
					if err != nil {
						found = false
					}
				}
			}
		}

		// Special fallbacks for Claude Code
		if !found && agent.Name == "Claude Code" {
			fallbacks := []string{"claude-code", "claude"}
			for _, fb := range fallbacks {
				if fb == agent.Command {
					continue
				}
				// Try both login and direct for each fallback
				for _, wrapper := range []string{"bash -lc 'command -v %s'", "command -v %s"} {
					_, err := s.executor.Execute(ctx, fmt.Sprintf(wrapper, fb))
					if err == nil {
						agent.Command = fb
						found = true
						break
					}
				}
				if found {
					break
				}
			}
		}

		// Special fallbacks for Cursor
		if !found && agent.Name == "Cursor" {
			fallbacks := []string{"cursor-tui", "cursor"}
			for _, fb := range fallbacks {
				if fb == agent.Command {
					continue
				}
				for _, wrapper := range []string{"bash -lc 'command -v %s'", "command -v %s"} {
					_, err := s.executor.Execute(ctx, fmt.Sprintf(wrapper, fb))
					if err == nil {
						agent.Command = fb
						found = true
						break
					}
				}
				if found {
					break
				}
			}
		}

		if found {
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
